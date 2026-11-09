package ipainfo

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// parsePlist decodes an XML or binary ("bplist00") property list into Go values:
// map[string]any, []any, string, int64, float64, bool, []byte; dates and UIDs become nil.
// Only what Info.plist needs; not a general-purpose implementation.
func parsePlist(data []byte) (any, error) {
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return parseBinaryPlist(data)
	}
	return parseXMLPlist(data)
}

func parseXMLPlist(data []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("xml plist: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local != "plist" {
			return parseXMLValue(dec, se)
		}
	}
}

func parseXMLValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		result := map[string]any{}
		key := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					key = k
					continue
				}
				v, err := parseXMLValue(dec, t)
				if err != nil {
					return nil, err
				}
				result[key] = v
			case xml.EndElement:
				return result, nil
			}
		}
	case "array":
		var result []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := parseXMLValue(dec, t)
				if err != nil {
					return nil, err
				}
				result = append(result, v)
			case xml.EndElement:
				return result, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	}
	var text string
	if err := dec.DecodeElement(&text, &se); err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	switch se.Name.Local {
	case "string":
		return text, nil
	case "integer":
		return strconv.ParseInt(text, 10, 64)
	case "real":
		return strconv.ParseFloat(text, 64)
	}
	return nil, nil // date, data: not needed
}

type binaryPlist struct {
	data        []byte
	offsets     []uint64
	objRefSize  int
	depthBudget int
}

func parseBinaryPlist(data []byte) (any, error) {
	if len(data) < 40 {
		return nil, errors.New("bplist: too short")
	}
	trailer := data[len(data)-32:]
	offsetSize := int(trailer[6])
	objRefSize := int(trailer[7])
	numObjects := binary.BigEndian.Uint64(trailer[8:16])
	topObject := binary.BigEndian.Uint64(trailer[16:24])
	tableOffset := binary.BigEndian.Uint64(trailer[24:32])
	if offsetSize < 1 || offsetSize > 8 || objRefSize < 1 || objRefSize > 8 ||
		numObjects > uint64(len(data)) || tableOffset > uint64(len(data)) ||
		tableOffset+numObjects*uint64(offsetSize) > uint64(len(data)) {
		return nil, errors.New("bplist: bad trailer")
	}
	p := &binaryPlist{data: data, objRefSize: objRefSize, depthBudget: 100000}
	p.offsets = make([]uint64, numObjects)
	for i := range p.offsets {
		start := tableOffset + uint64(i*offsetSize)
		p.offsets[i] = readUint(data[start : start+uint64(offsetSize)])
	}
	return p.object(topObject, 0)
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (p *binaryPlist) object(ref uint64, depth int) (any, error) {
	p.depthBudget--
	if ref >= uint64(len(p.offsets)) || depth > 64 || p.depthBudget < 0 {
		return nil, errors.New("bplist: bad object reference")
	}
	off := p.offsets[ref]
	if off >= uint64(len(p.data)) {
		return nil, io.ErrUnexpectedEOF
	}
	marker := p.data[off]
	kind, info := marker>>4, int(marker&0x0f)
	pos := off + 1

	// length of strings / data / collections
	length := func() (uint64, error) {
		if info != 0x0f {
			return uint64(info), nil
		}
		if pos >= uint64(len(p.data)) {
			return 0, io.ErrUnexpectedEOF
		}
		intMarker := p.data[pos]
		if intMarker>>4 != 0x1 {
			return 0, errors.New("bplist: bad length")
		}
		n := uint64(1) << (intMarker & 0x0f)
		if pos+1+n > uint64(len(p.data)) {
			return 0, io.ErrUnexpectedEOF
		}
		v := readUint(p.data[pos+1 : pos+1+n])
		pos += 1 + n
		return v, nil
	}
	slice := func(n uint64) ([]byte, error) {
		if n > uint64(len(p.data)) || pos+n > uint64(len(p.data)) {
			return nil, io.ErrUnexpectedEOF
		}
		return p.data[pos : pos+n], nil
	}

	switch kind {
	case 0x0:
		switch marker {
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		return nil, nil
	case 0x1:
		b, err := slice(uint64(1) << info)
		if err != nil {
			return nil, err
		}
		return int64(readUint(b)), nil
	case 0x2:
		b, err := slice(uint64(1) << info)
		if err != nil {
			return nil, err
		}
		if len(b) == 4 {
			return float64(math.Float32frombits(uint32(readUint(b)))), nil
		}
		return math.Float64frombits(readUint(b)), nil
	case 0x4:
		n, err := length()
		if err != nil {
			return nil, err
		}
		return slice(n)
	case 0x5:
		n, err := length()
		if err != nil {
			return nil, err
		}
		b, err := slice(n)
		return string(b), err
	case 0x6:
		n, err := length()
		if err != nil {
			return nil, err
		}
		b, err := slice(n * 2)
		if err != nil {
			return nil, err
		}
		units := make([]uint16, n)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(b[i*2:])
		}
		return string(utf16.Decode(units)), nil
	case 0xA, 0xD:
		n, err := length()
		if err != nil {
			return nil, err
		}
		count := n
		if kind == 0xD {
			count = n * 2
		}
		refs, err := slice(count * uint64(p.objRefSize))
		if err != nil {
			return nil, err
		}
		ref := func(i uint64) uint64 {
			return readUint(refs[i*uint64(p.objRefSize) : (i+1)*uint64(p.objRefSize)])
		}
		if kind == 0xA {
			result := make([]any, 0, n)
			for i := uint64(0); i < n; i++ {
				v, err := p.object(ref(i), depth+1)
				if err != nil {
					return nil, err
				}
				result = append(result, v)
			}
			return result, nil
		}
		result := make(map[string]any, n)
		for i := uint64(0); i < n; i++ {
			k, err := p.object(ref(i), depth+1)
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("bplist: non-string key")
			}
			v, err := p.object(ref(n+i), depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = v
		}
		return result, nil
	}
	return nil, nil // date, uid, set: not needed
}
