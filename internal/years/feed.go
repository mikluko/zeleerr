package years

import (
	"bytes"
	"encoding/xml"
	"io"
)

// RewriteTitles returns the Torznab feed with the title of each item passed
// through rename. Everything else, and every title rename leaves as it is,
// keeps its bytes.
func RewriteTitles(feed []byte, rename func(string) string) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(feed))
	var out bytes.Buffer
	var text bytes.Buffer
	last, start := 0, 0
	items, inTitle := 0, false
	for {
		before := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "item":
				items++
			case items > 0 && t.Name.Space == "" && t.Name.Local == "title":
				inTitle, start = true, int(d.InputOffset())
				text.Reset()
			}
		case xml.CharData:
			if inTitle {
				text.Write(t)
			}
		case xml.EndElement:
			switch {
			case inTitle && t.Name.Local == "title":
				inTitle = false
				if renamed := rename(text.String()); renamed != text.String() {
					out.Write(feed[last:start])
					if err := xml.EscapeText(&out, []byte(renamed)); err != nil {
						return nil, err
					}
					last = before
				}
			case t.Name.Local == "item":
				items--
			}
		}
	}
	out.Write(feed[last:])
	return out.Bytes(), nil
}
