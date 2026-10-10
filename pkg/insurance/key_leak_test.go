package insurance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unexportedPtrHolder struct {
	n      int
	client *Client
}

type unexportedValHolder struct{ client Client }

type ExportedPtrHolder struct{ Client *Client }

type nestedHolder struct{ inner unexportedPtrHolder }

// Issue #325: fmt does not call Format on unexported fields, so the key must
// not be stored in any field fmt can reach.
func TestClientKeyNeverPrintedWherever(t *testing.T) {
	c, err := newClient("http://127.0.0.1:1", Config{APIKey: testKey, OrgID: testOrg}, nil)
	require.NoError(t, err)

	holders := map[string]any{
		"unexported *Client":       unexportedPtrHolder{1, c},
		"&unexported *Client":      &unexportedPtrHolder{1, c},
		"unexported Client value":  unexportedValHolder{*c},
		"&unexported Client value": &unexportedValHolder{*c},
		"exported *Client":         ExportedPtrHolder{c},
		"nested":                   nestedHolder{unexportedPtrHolder{1, c}},
		"[]*Client":                []*Client{c},
		"map":                      map[string]*Client{"a": c},
		"[]any":                    []any{c, unexportedValHolder{*c}},
		"wrapped error":            fmt.Errorf("x: %w", fmt.Errorf("%v", unexportedValHolder{*c})),
	}
	forbidden := []string{
		testKey,
		fmt.Sprintf("%x", testKey), fmt.Sprintf("%X", testKey),
		base64.StdEncoding.EncodeToString([]byte(testKey)),
		fmt.Sprintf("%q", testKey),
	}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T", "%p"}
	for name, h := range holders {
		for _, verb := range append([]string{"Sprint", "Sprintln"}, verbs...) {
			t.Run(name+"/"+verb, func(t *testing.T) {
				var out string
				switch verb {
				case "Sprint":
					out = fmt.Sprint(h)
				case "Sprintln":
					out = fmt.Sprintln(h)
				default:
					out = fmt.Sprintf(verb, h)
				}
				for _, bad := range forbidden {
					assert.NotContains(t, out, bad)
				}
			})
		}
	}
	b, err := json.Marshal(ExportedPtrHolder{c})
	require.NoError(t, err)
	for _, bad := range forbidden {
		require.NotContains(t, string(b), bad)
	}
}

// The invariant must not depend on fmt behavior: walk every field of a
// Client, unexported ones included, and require that no string equals or
// contains the key.
func TestClientHoldsNoStringContainingKey(t *testing.T) {
	c, err := newClient("http://127.0.0.1:1", Config{APIKey: testKey, OrgID: testOrg}, nil)
	require.NoError(t, err)
	seen := map[uintptr]bool{}
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			require.NotContains(t, v.String(), testKey, path)
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return
			}
			if v.Kind() == reflect.Pointer {
				if seen[v.Pointer()] {
					return
				}
				seen[v.Pointer()] = true
			}
			walk(path+"*", v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
				require.NotContains(t, string(v.Bytes()), testKey, path)
				return
			}
			for i := 0; i < v.Len(); i++ {
				walk(fmt.Sprintf("%s[%d]", path, i), v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(path+"{key}", k)
				walk(path+"{val}", v.MapIndex(k))
			}
		}
	}
	walk("Client", reflect.ValueOf(c))
	require.True(t, strings.HasPrefix(c.baseURL, "http"), "walk reached the struct")
}
