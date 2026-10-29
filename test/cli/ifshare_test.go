package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ipfs/kubo/test/cli/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIfshareCLIAndRPCCompatibility(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()

	assert.Contains(t, node.IPFS("--help").Stdout.String(), "ifshare [")
	assert.Contains(t, node.IPFS("init", "--help").Stdout.String(), "ifshare init --profile=autoconf-on")
	assert.True(t, strings.HasPrefix(node.IPFS("version").Stdout.String(), "ifshare version "))
	assert.True(t, strings.HasPrefix(node.IPFS("version", "--all").Stdout.String(), "ifshare version: "))

	initOutput := node.IPFS("init").Stdout.String()
	assert.Contains(t, initOutput, "initializing ifshare node at ")
	node.IPFS("config", "Addresses.API", "/ip4/127.0.0.1/tcp/0")
	node.IPFS("config", "Addresses.Gateway", "/ip4/127.0.0.1/tcp/0")
	node.StartDaemon("--offline")
	assert.Contains(t, node.Daemon.Stdout.String(), "ifshare version: ")

	// User content is never subject to executable-name substitutions.
	content := "ipfs is file content: /ipfs/example and https://ipfs.tech\n"
	cid := node.IPFSAddStr(content)
	assert.Equal(t, content, node.IPFS("cat", cid).Stdout.String())

	for _, tc := range []struct {
		path   string
		prefix string
	}{
		{"version?encoding=text", "ipfs version "},
		{"version?encoding=text&all=true", "Kubo version: "},
		{"commands?encoding=text", "ipfs\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, node.APIURL()+"/api/v0/"+tc.path, nil)
			require.NoError(t, err)
			res, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer res.Body.Close()
			require.Equal(t, http.StatusOK, res.StatusCode)
			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(string(body), tc.prefix), "%s", body)
		})
	}

	var info map[string]any
	require.NoError(t, json.Unmarshal(node.IPFS("version", "--enc=json").Stdout.Bytes(), &info))
	for _, field := range []string{"Version", "Commit", "Repo", "System", "Golang"} {
		assert.Contains(t, info, field)
	}
}
