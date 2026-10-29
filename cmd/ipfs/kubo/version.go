package kubo

import (
	"bytes"
	"io"
	"maps"
	"strings"

	cmds "github.com/ipfs/go-ipfs-cmds"
	version "github.com/ipfs/kubo"
	"github.com/ipfs/kubo/core/commands"
)

// ifshareVersionCmd changes the CLI label while preserving the RPC encoders.
func ifshareVersionCmd() *cmds.Command {
	cmd := *commands.VersionCmd
	cmd.Encoders = maps.Clone(cmd.Encoders)
	cmd.Encoders[cmds.Text] = cmds.MakeTypedEncoder(func(req *cmds.Request, w io.Writer, info *version.VersionInfo) error {
		var buf bytes.Buffer
		if err := commands.VersionCmd.Encoders[cmds.Text](req)(&buf).Encode(info); err != nil {
			return err
		}
		output := buf.String()
		if strings.HasPrefix(output, "ipfs version ") {
			output = "ifshare version " + strings.TrimPrefix(output, "ipfs version ")
		} else if strings.HasPrefix(output, "Kubo version: ") {
			output = "ifshare version: " + strings.TrimPrefix(output, "Kubo version: ")
		}
		_, err := io.WriteString(w, output)
		return err
	})
	return &cmd
}
