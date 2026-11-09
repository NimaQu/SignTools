package main

import (
	"SignTools/src/assets"
	"SignTools/src/builders"
	"SignTools/src/config"
	"SignTools/src/portal"
	"SignTools/src/storage"
	htmlTemplate "html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
)

var portalClient *portal.Client

// setupLocalSigning prepares in-process signing (builder.local) and Apple ID support before serving.
func setupLocalSigning(port uint64) {
	builders.LocalServerUrl = "http://127.0.0.1:" + strconv.FormatUint(port, 10)

	if config.Current.AppleId.Enable {
		dataDir := config.Current.AppleId.DataDir
		if dataDir == "" {
			dataDir = filepath.Join(config.Current.SaveDir, "portal")
		}
		dataDir, _ = filepath.Abs(dataDir)
		if err := os.MkdirAll(dataDir, 0700); err != nil {
			log.Fatal().Err(err).Msg("create apple_id.data_dir")
		}
		var env []string
		if u := config.Current.AppleId.AnisetteUrl; u != "" {
			env = append(env, "ANISETTE_URL_V3="+u)
		}
		portalClient = &portal.Client{Bin: config.Current.AppleId.PortalBin, DataDir: dataDir, Env: env}
		// the signing script calls signtools-portal too
		_ = os.Setenv("SIGNTOOLS_PORTAL", config.Current.AppleId.PortalBin)
		_ = os.Setenv("PORTAL_DATA", dataDir)
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			_ = os.Setenv(k, v)
		}
	}

	for _, b := range config.Current.Builder {
		local, ok := b.(*builders.Local)
		if !ok {
			continue
		}
		if local.WorkDir == "" {
			local.WorkDir = filepath.Join(config.Current.SaveDir, "work")
		}
		local.WorkDir, _ = filepath.Abs(local.WorkDir)
		if err := os.MkdirAll(local.WorkDir, 0700); err != nil {
			log.Fatal().Err(err).Msg("create builder.local.work_dir")
		}
		local.TimeoutMins = config.Current.SignTimeoutMins
		// jobs read the unsigned app in place and hand the signed one back by path
		if config.Current.LocalTransferDir == "" {
			config.Current.LocalTransferDir = local.WorkDir
		}
	}
}

func registerAppleIdRoutes(e *echo.Echo, basicAuth func(echo.HandlerFunc) echo.HandlerFunc) {
	e.GET("/local-builder", renderLocalBuilder, basicAuth)
	if portalClient == nil {
		return
	}
	e.GET("/appleid", func(c echo.Context) error { return renderAppleId(c, appleIdPage{}) }, basicAuth)
	e.POST("/appleid/login", appleIdLogin, basicAuth)
	e.POST("/appleid/login/:id", appleIdSubmitTwoFactor, basicAuth)
	e.POST("/appleid/profiles", appleIdAddProfile, basicAuth)
	e.POST("/appleid/logout", appleIdLogout, basicAuth)
}

type appleIdTeam struct {
	portal.Team
	ProfileId string
}

type appleIdAccount struct {
	portal.Account
	Teams      []appleIdTeam
	TeamsError string
}

type appleIdTwoFactor struct {
	LoginId string
	Email   string
	Method  string
	Phones  []portal.Phone
}

type appleIdPage struct {
	Accounts  []appleIdAccount
	TwoFactor *appleIdTwoFactor
	Error     string
	Message   string
}

func renderAppleId(c echo.Context, page appleIdPage) error {
	if page.TwoFactor == nil {
		accounts, err := portalClient.Accounts()
		if err != nil && page.Error == "" {
			page.Error = err.Error()
		}
		for _, account := range accounts {
			entry := appleIdAccount{Account: account}
			teams, err := portalClient.Teams(account.Email)
			if err != nil {
				entry.TeamsError = err.Error()
			}
			for _, team := range teams {
				t := appleIdTeam{Team: team}
				id := storage.AppleIdProfileId(account.Email, team.Id)
				if _, ok := storage.Profiles.GetById(id); ok {
					t.ProfileId = id
				}
				entry.Teams = append(entry.Teams, t)
			}
			page.Accounts = append(page.Accounts, entry)
		}
	}
	if page.Message == "" {
		page.Message = c.QueryParam("message")
	}
	t, err := htmlTemplate.New("").Parse(assets.AppleIdHtml)
	if err != nil {
		return err
	}
	return t.Execute(c.Response(), page)
}

func appleIdLoginResult(c echo.Context, login *portal.Login, ev *portal.Event, err error) error {
	if err != nil {
		return renderAppleId(c, appleIdPage{Error: "Login failed: " + err.Error()})
	}
	switch ev.Event {
	case "2fa":
		return renderAppleId(c, appleIdPage{TwoFactor: &appleIdTwoFactor{
			LoginId: login.Id, Email: login.Email, Method: ev.Method, Phones: ev.Phones,
		}})
	case "done":
		return c.Redirect(http.StatusFound, "/appleid?message="+url.QueryEscape("Logged in as "+ev.Email+", pick the teams to sign with"))
	default:
		return renderAppleId(c, appleIdPage{Error: "Login failed: " + ev.Error})
	}
}

func appleIdLogin(c echo.Context) error {
	email := strings.TrimSpace(c.FormValue("email"))
	password := c.FormValue("password")
	if email == "" || password == "" {
		return renderAppleId(c, appleIdPage{Error: "Apple ID and password are required"})
	}
	login, ev, err := portalClient.StartLogin(email, password)
	return appleIdLoginResult(c, login, ev, err)
}

func appleIdSubmitTwoFactor(c echo.Context) error {
	login, ok := portalClient.GetLogin(c.Param("id"))
	if !ok {
		return renderAppleId(c, appleIdPage{Error: "This login expired, please log in again"})
	}
	var ev *portal.Event
	var err error
	if sms := c.FormValue("sms"); sms != "" {
		phoneId, convErr := strconv.Atoi(sms)
		if convErr != nil {
			return c.String(http.StatusBadRequest, "invalid phone")
		}
		ev, err = portalClient.RequestSms(login, phoneId)
	} else {
		ev, err = portalClient.SubmitCode(login, strings.TrimSpace(c.FormValue("code")))
	}
	return appleIdLoginResult(c, login, ev, err)
}

func appleIdAddProfile(c echo.Context) error {
	email := c.FormValue("email")
	teamId := c.FormValue("team_id")
	teams, err := portalClient.Teams(email)
	if err != nil {
		return renderAppleId(c, appleIdPage{Error: err.Error()})
	}
	var team *portal.Team
	for i := range teams {
		if teams[i].Id == teamId {
			team = &teams[i]
		}
	}
	if team == nil {
		return renderAppleId(c, appleIdPage{Error: "team " + teamId + " not found in " + email})
	}
	name := team.Name + " (" + team.Id + ") · " + email
	if _, err := storage.CreateAppleIdProfile(email, team.Id, name, c.FormValue("cert_reset") != ""); err != nil {
		return renderAppleId(c, appleIdPage{Error: err.Error()})
	}
	return c.Redirect(http.StatusFound, "/appleid?message="+url.QueryEscape("Added signing profile "+name))
}

func appleIdLogout(c echo.Context) error {
	email := c.FormValue("email")
	if err := portalClient.Logout(email); err != nil {
		return renderAppleId(c, appleIdPage{Error: err.Error()})
	}
	if err := storage.DeleteAppleIdProfiles(email); err != nil {
		return renderAppleId(c, appleIdPage{Error: err.Error()})
	}
	return c.Redirect(http.StatusFound, "/appleid?message="+url.QueryEscape("Logged out "+email))
}

type localJobView struct {
	builders.LocalJobLog
	Output   string
	Duration string
}

func renderLocalBuilder(c echo.Context) error {
	var views []localJobView
	for _, b := range config.Current.Builder {
		local, ok := b.(*builders.Local)
		if !ok {
			continue
		}
		for _, entry := range local.Logs() {
			views = append(views, localJobView{
				LocalJobLog: entry,
				Output:      entry.Output.String(),
				Duration:    entry.Finished.Sub(entry.Started).Round(time.Second).String(),
			})
		}
	}
	t, err := htmlTemplate.New("").Parse(assets.LocalBuilderHtml)
	if err != nil {
		return err
	}
	return t.Execute(c.Response(), views)
}
