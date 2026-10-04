package cli

import (
	"context"
	"io"
	"os"

	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

var spritesLoginStdin io.Reader = os.Stdin

func init() {
	Register(Command{
		Name:    "sprites",
		Summary: "Manage the stored Sprites API token (login|logout)",
		Run:     runSpritesAuth,
	})
}

func runSpritesAuth(_ context.Context, args []string, out *Output) error {
	if len(args) != 1 || (args[0] != "login" && args[0] != "logout") {
		return &UsageError{Msg: "sprites: usage: sprites <login|logout>  (login reads the token from stdin)"}
	}
	if args[0] == "logout" {
		if err := sprites.RemoveToken(); err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: err.Error()}
		}
		out.EmitSuccess("sprites.logout", nil, "sprites token removed")
		return nil
	}
	in, err := io.ReadAll(io.LimitReader(spritesLoginStdin, 64<<10))
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "sprites login: read stdin: " + err.Error()}
	}
	tok, err := sprites.ParseToken(string(in))
	if err != nil {
		return &UsageError{Msg: "sprites login: " + err.Error()}
	}
	if err := sprites.SaveToken(tok); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "sprites login: " + err.Error()}
	}
	p, _ := sprites.TokenPath()
	out.EmitSuccess("sprites.login", nil, "sprites token saved to "+p)
	return nil
}
