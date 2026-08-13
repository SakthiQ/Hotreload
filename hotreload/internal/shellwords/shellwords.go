// Package shellwords splits a command string into arguments the way a shell
// would, honouring single and double quotes.
//
// Backslashes are treated literally rather than as escape characters, because
// on Windows they are path separators and the primary consumer of this package
// is the Windows runner, which must exec the child binary directly instead of
// going through cmd.exe.
package shellwords

import "fmt"

// Split breaks s into arguments on unquoted whitespace. Quoted runs are kept
// together and the surrounding quotes are removed. An unterminated quote is an
// error rather than a silent truncation, so the user sees the typo.
func Split(s string) ([]string, error) {
	var (
		args    []string
		current []rune
		quote   rune
		started bool
	)

	flush := func() {
		if started {
			args = append(args, string(current))
			current = current[:0]
			started = false
		}
	}

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current = append(current, r)
			}

		case r == '\'' || r == '"':
			quote = r
			// An empty quoted string is still an argument.
			started = true

		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()

		default:
			current = append(current, r)
			started = true
		}
	}

	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in command: %s", quote, s)
	}
	flush()

	return args, nil
}
