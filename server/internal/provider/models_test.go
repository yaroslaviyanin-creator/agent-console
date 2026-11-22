package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReadModelCatalog(t *testing.T) {
	fixture := strings.NewReader(`
{"id":0,"result":{}}
{"method":"account/updated","params":{}}
{"id":1,"result":{"data":[{"id":"internal","model":"gpt-first","displayName":"First model","isDefault":true},{"model":"hidden","hidden":true}],"nextCursor":"another-page"}}
{"id":2,"result":{"data":[{"model":"gpt-first"},{"model":"gpt-next"}],"nextCursor":null}}
`[1:])
	var requests bytes.Buffer
	models, err := readModelCatalog(&requests, fixture)
	want := []Model{{ID: "gpt-first", Name: "First model", IsDefault: true}, {ID: "gpt-next", Name: "gpt-next"}}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v, error = %v", models, err)
	}
	decoder := json.NewDecoder(&requests)
	for i, method := range []string{"initialize", "initialized", "model/list", "model/list"} {
		var req struct {
			Method string
			Params struct {
				Cursor        string
				IncludeHidden bool
			}
		}
		if err := decoder.Decode(&req); err != nil || req.Method != method || req.Params.IncludeHidden {
			t.Fatalf("request = %#v, error = %v", req, err)
		}
		wantCursor := ""
		if i == 3 {
			wantCursor = "another-page"
		}
		if req.Params.Cursor != wantCursor {
			t.Fatalf("cursor = %q, want %q", req.Params.Cursor, wantCursor)
		}
	}
}

func TestReadModelCatalogRejectsRPCErrorAndRepeatedCursor(t *testing.T) {
	for _, fixture := range []string{
		"{\"id\":0,\"result\":{}}\n{\"id\":1,\"error\":{\"message\":\"test error\"}}\n",
		"{\"id\":0,\"result\":{}}\n{\"id\":1,\"result\":{\"data\":[],\"nextCursor\":\"same\"}}\n{\"id\":2,\"result\":{\"data\":[],\"nextCursor\":\"same\"}}\n",
	} {
		var requests bytes.Buffer
		if _, err := readModelCatalog(&requests, strings.NewReader(fixture)); err == nil {
			t.Fatal("invalid reply accepted")
		}
	}
}
