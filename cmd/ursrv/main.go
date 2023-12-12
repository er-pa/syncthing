// Copyright (C) 2023 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"log"
	"os"

	"github.com/alecthomas/kong"
	"github.com/syncthing/syncthing/cmd/ursrv/aggregate"
	"github.com/syncthing/syncthing/cmd/ursrv/blob"
	"github.com/syncthing/syncthing/cmd/ursrv/serve"
)

type CLI struct {
	Serve     serve.CLI     `cmd:"" default:""`
	Aggregate aggregate.CLI `cmd:""`
}

func main() {
	log.SetFlags(log.Ltime | log.Ldate | log.Lshortfile)
	log.SetOutput(os.Stdout)

	// Initialize the blob storage used to store the temporary usage reports and
	// the persistent aggregated reports.
	b := blob.NewBlobStorage()
	store := blob.NewUrsrvStore(b)

	var cli CLI
	ctx := kong.Parse(&cli)
	if err := ctx.Run(store); err != nil {
		log.Fatalf("%s: %v", ctx.Command(), err)
	}
}
