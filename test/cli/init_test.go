package cli

import (
	"crypto/rand"
	"fmt"
	"os"
	fp "path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/test/cli/harness"
	. "github.com/ipfs/kubo/test/cli/testutils"
	pb "github.com/libp2p/go-libp2p/core/crypto/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validatePeerID(t *testing.T, peerID peer.ID, expErr error, expAlgo pb.KeyType) {
	assert.NoError(t, peerID.Validate())
	pub, err := peerID.ExtractPublicKey()
	assert.ErrorIs(t, expErr, err)
	if expAlgo != 0 {
		assert.Equal(t, expAlgo, pub.Type())
	}
}

func testInitAlgo(t *testing.T, initFlags []string, expOutputName string, expPeerIDPubKeyErr error, expPeerIDPubKeyType pb.KeyType) {
	t.Run("init", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode()
		initRes := node.IPFS(StrCat("init", initFlags)...)

		lines := []string{
			fmt.Sprintf("generating %s keypair...done", expOutputName),
			fmt.Sprintf("peer identity: %s", node.PeerID().String()),
			fmt.Sprintf("initializing ifshare node at %s\n", node.Dir),
		}
		expectedInitOutput := strings.Join(lines, "\n")
		assert.Equal(t, expectedInitOutput, initRes.Stdout.String())

		assert.DirExists(t, node.Dir)
		assert.FileExists(t, fp.Join(node.Dir, "config"))
		assert.DirExists(t, fp.Join(node.Dir, "datastore"))
		assert.DirExists(t, fp.Join(node.Dir, "blocks"))
		assert.NoFileExists(t, fp.Join(node.Dir, "._check_writeable"))

		_, err := os.ReadDir(node.Dir)
		assert.NoError(t, err, "ifshare dir should be listable")

		validatePeerID(t, node.PeerID(), expPeerIDPubKeyErr, expPeerIDPubKeyType)

		res := node.IPFS("config", "Mounts.IPFS")
		assert.Equal(t, "/ipfs", res.Stdout.Trimmed())

		catRes := node.RunIPFS("cat", fmt.Sprintf("/ipfs/%s/readme", CIDWelcomeDocs))
		assert.NotEqual(t, 0, catRes.ExitErr.ExitCode(), "welcome readme doesn't exist")
	})

	t.Run("init without empty repo", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode()
		initRes := node.IPFS(StrCat("init", "--empty-repo=false", initFlags)...)

		validatePeerID(t, node.PeerID(), expPeerIDPubKeyErr, expPeerIDPubKeyType)

		lines := []string{
			fmt.Sprintf("generating %s keypair...done", expOutputName),
			fmt.Sprintf("peer identity: %s", node.PeerID().String()),
			fmt.Sprintf("initializing ifshare node at %s", node.Dir),
			"to get started, enter:",
			fmt.Sprintf("\n\tifshare cat /ipfs/%s/readme\n\n", CIDWelcomeDocs),
		}
		expectedEmptyInitOutput := strings.Join(lines, "\n")
		assert.Equal(t, expectedEmptyInitOutput, initRes.Stdout.String())

		node.IPFS("cat", fmt.Sprintf("/ipfs/%s/readme", CIDWelcomeDocs))

		idRes := node.IPFS("id", "-f", "<aver>")
		version := node.IPFS("version", "-n").Stdout.Trimmed()
		assert.Contains(t, idRes.Stdout.String(), version)
	})
}

func TestInit(t *testing.T) {
	t.Parallel()

	t.Run("init fails if the repo dir has no perms", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode()
		badDir := fp.Join(node.Dir, ".badipfs")
		err := os.Mkdir(badDir, 0o000)
		require.NoError(t, err)

		res := node.RunIPFS("init", "--repo-dir", badDir)
		assert.NotEqual(t, 0, res.Cmd.ProcessState.ExitCode())
		assert.Contains(t, res.Stderr.String(), "permission denied")
	})

	t.Run("init with ed25519", func(t *testing.T) {
		t.Parallel()
		testInitAlgo(t, []string{"--algorithm=ed25519"}, "ED25519", nil, pb.KeyType_Ed25519)
	})

	t.Run("init with rsa", func(t *testing.T) {
		t.Parallel()
		testInitAlgo(t, []string{"--bits=2048", "--algorithm=rsa"}, "2048-bit RSA", peer.ErrNoPublicKey, 0)
	})

	t.Run("init with default algorithm", func(t *testing.T) {
		t.Parallel()
		testInitAlgo(t, []string{}, "ED25519", nil, pb.KeyType_Ed25519)
	})

	t.Run("ifshare init --profile with invalid profile fails", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode()
		res := node.RunIPFS("init", "--profile=invalid_profile")
		assert.NotEqual(t, 0, res.ExitErr.ExitCode())
		assert.Equal(t, "Error: invalid configuration profile: invalid_profile", res.Stderr.Trimmed())
	})

	t.Run("ifshare init --profile with valid profile succeeds", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode()
		node.IPFS("init", "--profile=server")
	})

	t.Run("ifshare config looks good", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode().Init("--profile=server")

		lines := node.IPFS("config", "Swarm.AddrFilters").Stdout.Lines()
		assert.Len(t, lines, 21)

		out := node.IPFS("config", "Bootstrap").Stdout.Trimmed()
		assert.Equal(t, "[]", out)

		out = node.IPFS("config", "Addresses.API").Stdout.Trimmed()
		assert.Equal(t, "/ip4/127.0.0.1/tcp/0", out)
	})

	t.Run("ifshare init from existing config succeeds", func(t *testing.T) {
		t.Parallel()
		nodes := harness.NewT(t).NewNodes(2)
		node1 := nodes[0]
		node2 := nodes[1]

		node1.Init("--profile=server")

		node2.IPFS("init", fp.Join(node1.Dir, "config"))
		out := node2.IPFS("config", "Addresses.API").Stdout.Trimmed()
		assert.Equal(t, "/ip4/127.0.0.1/tcp/0", out)
	})

	t.Run("ifshare init from 'ifshare config show' output fails", func(t *testing.T) {
		t.Parallel()
		nodes := harness.NewT(t).NewNodes(2)
		nodes[0].Init()
		// config show strips the private key, so the file cannot seed a repo
		shown := nodes[0].IPFS("config", "show").Stdout.String()
		path := fp.Join(nodes[1].Dir, "shown.json")
		require.NoError(t, os.WriteFile(path, []byte(shown), 0o600))

		res := nodes[1].RunIPFS("init", path)
		require.NotEqual(t, 0, res.ExitCode())
		assert.Contains(t, res.Stderr.String(), "Identity.PrivKey")
		assert.NoFileExists(t, fp.Join(nodes[1].Dir, "config"))

		// the daemon's --init path takes the same file and must refuse it too
		res = nodes[1].RunIPFS("daemon", "--init", "--init-config="+path, "--offline")
		require.NotEqual(t, 0, res.ExitCode())
		assert.Contains(t, res.Stderr.String(), "Identity.PrivKey")
		assert.NoFileExists(t, fp.Join(nodes[1].Dir, "config"))
	})

	t.Run("ifshare init should not run while daemon is running", func(t *testing.T) {
		t.Parallel()
		node := harness.NewT(t).NewNode().Init().StartDaemon()
		defer node.StopDaemon()
		res := node.RunIPFS("init")
		assert.NotEqual(t, 0, res.ExitErr.ExitCode())
		assert.Contains(t, res.Stderr.String(), "Error: ifshare daemon is running. please stop it to run this command")
	})
}

func TestInitNetworkingDefaults(t *testing.T) {
	t.Parallel()

	for _, profile := range []string{"", "server", "autoconf-on"} {
		name := profile
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			node := harness.NewT(t).NewNode()
			args := []string{"init"}
			if profile != "" {
				args = append(args, "--profile="+profile)
			}
			// Use the CLI directly: Node.Init clears Bootstrap for test isolation.
			node.IPFS(args...)
			cfg := node.ReadConfig()

			if profile == "autoconf-on" {
				assert.True(t, cfg.AutoConf.Enabled.WithDefault(config.DefaultAutoConfEnabled))
				assert.Equal(t, []string{config.AutoPlaceholder}, cfg.Bootstrap)
				assert.Equal(t, map[string]string{".": config.AutoPlaceholder}, cfg.DNS.Resolvers)
				assert.Equal(t, []string{config.AutoPlaceholder}, cfg.Routing.DelegatedRouters)
				assert.Equal(t, []string{config.AutoPlaceholder}, cfg.Ipns.DelegatedPublishers)
				return
			}

			assert.False(t, cfg.AutoConf.Enabled.WithDefault(config.DefaultAutoConfEnabled))
			assert.Equal(t, []string{}, cfg.Bootstrap)
			assert.Equal(t, map[string]string{}, cfg.DNS.Resolvers)
			assert.Equal(t, []string{}, cfg.Routing.DelegatedRouters)
			assert.Equal(t, []string{}, cfg.Ipns.DelegatedPublishers)
			assert.Equal(t, "[]", node.IPFS("config", "Bootstrap").Stdout.Trimmed())
			assert.Empty(t, node.IPFS("bootstrap", "list").Stdout.Trimmed())
			assert.Empty(t, node.IPFS("bootstrap", "list", "--expand-auto").Stdout.Trimmed())
		})
	}
}

func TestInitPrivateNetworkBootstrap(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	swarmKey := []byte(fmt.Sprintf("/key/swarm/psk/1.0.0/\n/base16/\n%x\n", key))

	nodes := harness.NewT(t).NewNodes(2)
	for _, node := range nodes {
		// Keep the initialized Bootstrap and AutoConf values under test.
		node.IPFS("init")
		require.Equal(t, []string{}, node.ReadConfig().Bootstrap)
		node.UpdateConfig(func(cfg *config.Config) {
			cfg.Addresses.Swarm = []string{"/ip4/127.0.0.1/tcp/0"}
			cfg.Addresses.API = config.Strings{"/ip4/127.0.0.1/tcp/0"}
			cfg.Addresses.Gateway = config.Strings{"/ip4/127.0.0.1/tcp/0"}
			cfg.Discovery.MDNS.Enabled = false
			cfg.Swarm.DisableNatPortMap = true
			cfg.AutoTLS.Enabled = config.False
			cfg.Routing.Type = config.NewOptionalString("dht")
			cfg.Routing.LoopbackAddressesOnLanDHT = config.True
		})
		require.NoError(t, os.WriteFile(fp.Join(node.Dir, "swarm.key"), swarmKey, 0o600))
		node.Runner.Env["LIBP2P_FORCE_PNET"] = "1"
		node.Runner.Env["IPFS_TELEMETRY"] = "off"
	}

	nodes[0].StartDaemon()
	assert.Empty(t, nodes[0].Peers())
	addrs := nodes[0].SwarmAddrsWithPeerIDs()
	require.NotEmpty(t, addrs)
	bootstrapAddr := addrs[0].String()
	nodes[1].IPFS("bootstrap", "add", bootstrapAddr)
	assert.Equal(t, []string{bootstrapAddr}, nodes[1].ReadConfig().Bootstrap)
	nodes[1].StartDaemon()

	require.Eventually(t, func() bool {
		return len(nodes[0].Peers()) == 1 && len(nodes[1].Peers()) == 1
	}, 10*time.Second, 100*time.Millisecond)

	content := "content shared over the private network"
	cid := nodes[0].IPFSAddStr(content)
	assert.Equal(t, content, nodes[1].IPFS("cat", "--timeout=10s", cid).Stdout.String())
}
