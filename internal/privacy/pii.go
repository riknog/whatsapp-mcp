package privacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
)

// jidAssert matches a WhatsApp JID server after "@". It is case-insensitive
// and needs a word boundary, so "x@lidl.com" does not match.
var jidAssert = regexp.MustCompile(`(?i)@(?:s\.whatsapp\.net|c\.us|lid|g\.us)\b`)

// maxPlainInteger is the smallest magnitude of a number with 8 integer digits.
// Numbers at or above it are suspect: outputs use ISO-8601 strings for times.
const maxPlainInteger = 1e7

// AssertNoPII serializes v to JSON and checks every string, key and number in
// it. It returns an error when it finds:
//   - a WhatsApp JID (@s.whatsapp.net, @c.us, @lid, @g.us);
//   - a phone number that is not masked (the same detection as RedactText, so
//     masked forms such as "+55 11 9****-**21" pass);
//   - an e-mail address, a CPF or a CNPJ that is not masked (the detection of
//     RedactDocuments, so "j***@x.com" and "***.***.***-09" pass);
//   - a number of 8 or more integer digits, wherever it is in the tree.
//
// The error names the JSON path only. It never includes the value, so the
// error itself cannot leak it. Used by the tests of the tools.
func AssertNoPII(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("privacy: serializar saída: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return fmt.Errorf("privacy: ler saída serializada: %w", err)
	}
	return walkPII("$", tree)
}

func walkPII(path string, node any) error {
	switch x := node.(type) {
	case string:
		return checkPIIString(path, x)
	case json.Number:
		return checkPIINumber(path, x)
	case []any:
		for i, e := range x {
			if err := walkPII(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := checkPIIString(path+" (chave)", k); err != nil {
				return err
			}
			if err := walkPII(path+"."+k, x[k]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPIIString(path, s string) error {
	if jidAssert.MatchString(s) {
		return fmt.Errorf("privacy: JID encontrado em %s", path)
	}
	if hasReadablePhone(s) {
		return fmt.Errorf("privacy: telefone não mascarado em %s", path)
	}
	if hasReadableDocument(s) {
		return fmt.Errorf("privacy: e-mail, CPF ou CNPJ não mascarado em %s", path)
	}
	return nil
}

func checkPIINumber(path string, n json.Number) error {
	f, err := n.Float64()
	if err != nil {
		return fmt.Errorf("privacy: número inválido em %s", path)
	}
	if math.Abs(f) >= maxPlainInteger {
		return fmt.Errorf("privacy: número com 8 ou mais dígitos em %s (use texto ISO-8601 para datas)", path)
	}
	return nil
}
