package objectcmd

import (
	"errors"

	cmds "github.com/ipfs/go-ipfs-cmds"
)

type Link struct {
	Name, Hash string
	Size       uint64
}

type Object struct {
	Hash  string `json:"Hash,omitempty"`
	Links []Link `json:"Links,omitempty"`
}

var ErrDataEncoding = errors.New("unknown data field encoding")

var ObjectCmd = &cmds.Command{
	Status: cmds.Deprecated, // https://github.com/ipfs/kubo/issues/7936
	Helptext: cmds.HelpText{
		Tagline: "Deprecated commands to interact with dag-pb objects. Use 'dag' or 'files' instead.",
		ShortDescription: `
'ifshare object' is a legacy plumbing command used to manipulate dag-pb
objects
directly. Deprecated, use more modern 'ifshare dag' and 'ifshare files'
instead.`,
	},

	Subcommands: map[string]*cmds.Command{
		"data":  RemovedObjectCmd,
		"diff":  ObjectDiffCmd,
		"get":   RemovedObjectCmd,
		"links": RemovedObjectCmd,
		"new":   RemovedObjectCmd,
		"patch": ObjectPatchCmd,
		"put":   RemovedObjectCmd,
		"stat":  RemovedObjectCmd,
	},
}

var RemovedObjectCmd = &cmds.Command{
	Status: cmds.Removed,
	Helptext: cmds.HelpText{
		Tagline: "Removed, use 'ifshare dag' or 'ifshare files' instead.",
	},
	Run: func(req *cmds.Request, res cmds.ResponseEmitter, env cmds.Environment) error {
		return errors.New("removed, use 'ifshare dag' or 'ifshare files' instead")
	},
}
