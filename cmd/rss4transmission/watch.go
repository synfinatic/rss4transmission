package main

import (
	"time"
)

func (cmd *WatchCmd) Run(ctx *RunContext) error {

	once := OnceCmd{
		Feed: ctx.Cli.Watch.Feed,
	}

	// Run the first time...
	if err := once.Run(ctx); err != nil {
		return err
	}

	// ...and then sleep and repeat
	ticker := time.NewTicker(time.Duration(ctx.Cli.Watch.Sleep) * time.Second)
	for {
		select {
		case <-ticker.C:
			if err := once.Run(ctx); err != nil {
				return err
			}
		}
	}
}
