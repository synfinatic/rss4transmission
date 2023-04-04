package main

import (
	log "github.com/sirupsen/logrus"
)

func (cmd *OnceCmd) Run(ctx *RunContext) error {
	log.Debugf("Starting our run...")

	return nil
}
