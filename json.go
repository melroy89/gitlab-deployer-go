package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
)

func marshalJSONFile(w io.Writer, value any) error {
	encoder := jsontext.NewEncoder(w, jsontext.WithIndent("  "))
	return json.MarshalEncode(encoder, value)
}
