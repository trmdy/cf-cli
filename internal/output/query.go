package output

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// ApplyQuery runs a jq expression over a JSON value and returns the result.
// A query yielding exactly one value returns that value; multiple values are
// collected into an array (like jq -s over the output stream).
func ApplyQuery(raw json.RawMessage, query string) (json.RawMessage, error) {
	q, err := gojq.Parse(query)
	if err != nil {
		return nil, fmt.Errorf("invalid --query expression: %w", err)
	}
	var input any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, fmt.Errorf("--query: result is not JSON: %w", err)
		}
	}
	var results []any
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return nil, fmt.Errorf("--query: %w", err)
		}
		results = append(results, v)
	}
	var out any
	switch len(results) {
	case 0:
		out = nil
	case 1:
		out = results[0]
	default:
		out = results
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
