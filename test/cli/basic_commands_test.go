package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/ipfs/kubo/test/cli/harness"
	. "github.com/ipfs/kubo/test/cli/testutils"
	"github.com/stretchr/testify/assert"
	gomod "golang.org/x/mod/module"
)

var versionRegexp = regexp.MustCompile(`^ifshare version (.+)$`)

func parseVersionOutput(s string) semver.Version {
	versString := versionRegexp.FindStringSubmatch(s)[1]
	v, err := semver.Parse(versString)
	if err != nil {
		panic(err)
	}
	return v
}

func TestCurDirIsWritable(t *testing.T) {
	t.Parallel()
	h := harness.NewT(t)
	h.WriteFile("test.txt", "It works!")
}

func TestIPFSVersionCommandMatchesFlag(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	commandVersionStr := node.IPFS("version").Stdout.String()
	commandVersionStr = strings.TrimSpace(commandVersionStr)
	commandVersion := parseVersionOutput(commandVersionStr)

	flagVersionStr := node.IPFS("--version").Stdout.String()
	flagVersionStr = strings.TrimSpace(flagVersionStr)
	flagVersion := parseVersionOutput(flagVersionStr)

	assert.Equal(t, commandVersion, flagVersion)
}

func TestIPFSVersionAll(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	res := node.IPFS("version", "--all").Stdout.String()
	res = strings.TrimSpace(res)
	assert.Contains(t, res, "ifshare version")
	assert.Contains(t, res, "Repo version")
	assert.Contains(t, res, "System version")
	assert.Contains(t, res, "Golang version")
}

func TestIPFSVersionDeps(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	res := node.IPFS("version", "deps").Stdout.String()
	res = strings.TrimSpace(res)
	lines := SplitLines(res)

	assert.True(t, strings.HasPrefix(lines[0], "github.com/ipfs/kubo@v"))

	for _, depLine := range lines[1:] {
		split := strings.SplitSeq(depLine, " => ")
		for moduleVersion := range split {
			splitModVers := strings.Split(moduleVersion, "@")
			modPath := splitModVers[0]
			modVers := splitModVers[1]
			// Skip local replace paths (starting with "./")
			if strings.HasPrefix(modPath, "./") {
				continue
			}
			assert.NoError(t, gomod.Check(modPath, modVers), "path: %s, version: %s", modPath, modVers)
		}
	}
}

func TestIPFSCommands(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	cmds := node.IPFSCommands()
	assert.Contains(t, cmds, "ifshare add")
	assert.Contains(t, cmds, "ifshare daemon")
	assert.Contains(t, cmds, "ifshare update")
}

func TestAllSubcommandsAcceptHelp(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	for _, cmd := range node.IPFSCommands() {
		t.Run(fmt.Sprintf("command %q accepts help", cmd), func(t *testing.T) {
			t.Parallel()
			splitCmd := strings.Split(cmd, " ")[1:]
			node.IPFS(StrCat("help", splitCmd)...)
			node.IPFS(StrCat(splitCmd, "--help")...)
		})
	}
}

func TestAllRootCommandsAreMentionedInHelpText(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	cmds := node.IPFSCommands()
	var rootCmds []string
	for _, cmd := range cmds {
		splitCmd := strings.Split(cmd, " ")
		if len(splitCmd) == 2 {
			rootCmds = append(rootCmds, splitCmd[1])
		}
	}

	// a few base commands are not expected to be in the help message
	// but we default to requiring them to be in the help message, so that we
	// have to make a conscious decision to exclude them
	notInHelp := map[string]bool{
		"object":   true,
		"shutdown": true,
	}

	helpMsg := strings.TrimSpace(node.IPFS("--help").Stdout.String())
	for _, rootCmd := range rootCmds {
		if _, ok := notInHelp[rootCmd]; ok {
			continue
		}
		assert.Contains(t, helpMsg, fmt.Sprintf("  %s", rootCmd))
	}
}

func TestCommandDocsWidth(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()

	// require new commands to explicitly opt in to longer lines
	allowList := map[string]bool{
		"ifshare add":                      true,
		"ifshare block put":                true,
		"ifshare daemon":                   true,
		"ifshare config profile":           true,
		"ifshare pin remote service":       true,
		"ifshare name pubsub":              true,
		"ifshare object patch":             true,
		"ifshare swarm connect":            true,
		"ifshare p2p forward":              true,
		"ifshare p2p close":                true,
		"ifshare swarm disconnect":         true,
		"ifshare swarm addrs listen":       true,
		"ifshare dag resolve":              true,
		"ifshare dag get":                  true,
		"ifshare pin remote add":           true,
		"ifshare config show":              true,
		"ifshare config edit":              true,
		"ifshare pin remote rm":            true,
		"ifshare pin remote ls":            true,
		"ifshare pin verify":               true,
		"ifshare pin remote service add":   true,
		"ifshare pin update":               true,
		"ifshare pin rm":                   true,
		"ifshare p2p":                      true,
		"ifshare resolve":                  true,
		"ifshare dag stat":                 true,
		"ifshare name publish":             true,
		"ifshare object diff":              true,
		"ifshare object patch add-link":    true,
		"ifshare name":                     true,
		"ifshare diag profile":             true,
		"ifshare diag cmds":                true,
		"ifshare swarm addrs local":        true,
		"ifshare files ls":                 true,
		"ifshare stats bw":                 true,
		"ifshare swarm peers":              true,
		"ifshare pubsub sub":               true,
		"ifshare files write":              true,
		"ifshare swarm limit":              true,
		"ifshare commands completion fish": true,
		"ifshare key export":               true,
		"ifshare routing get":              true,
		"ifshare refs":                     true,
		"ifshare refs local":               true,
		"ifshare cid base32":               true,
		"ifshare pubsub pub":               true,
		"ifshare repo ls":                  true,
		"ifshare routing put":              true,
		"ifshare key import":               true,
		"ifshare swarm peering add":        true,
		"ifshare swarm peering rm":         true,
		"ifshare swarm peering ls":         true,
		"ifshare update":                   true,
		"ifshare swarm stats":              true,
	}
	for _, cmd := range node.IPFSCommands() {
		if _, ok := allowList[cmd]; ok {
			continue
		}
		t.Run(fmt.Sprintf("command %q conforms to docs width limit", cmd), func(t *testing.T) {
			splitCmd := strings.Split(cmd, " ")
			resStr := node.IPFS(StrCat(splitCmd[1:], "--help")...)
			res := strings.TrimSpace(resStr.Stdout.String())
			for _, line := range SplitLines(res) {
				assert.LessOrEqualf(t, len(line), 80, "expected width %d < 80 for %q", len(line), cmd)
			}
		})
	}
}

func TestAllCommandsFailWhenPassedBadFlag(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()

	for _, cmd := range node.IPFSCommands() {
		t.Run(fmt.Sprintf("command %q fails when passed a bad flag", cmd), func(t *testing.T) {
			splitCmd := strings.Split(cmd, " ")
			res := node.RunIPFS(StrCat(splitCmd, "--badflag")...)
			assert.Equal(t, 1, res.Cmd.ProcessState.ExitCode())
		})
	}
}

func TestCommandsFlags(t *testing.T) {
	t.Parallel()
	node := harness.NewT(t).NewNode()
	resStr := node.IPFS("commands", "--flags").Stdout.String()
	assert.Contains(t, resStr, "ifshare pin add --recursive / ifshare pin add -r")
	assert.Contains(t, resStr, "ifshare id --format / ifshare id -f")
	assert.Contains(t, resStr, "ifshare repo gc --quiet / ifshare repo gc -q")
}
