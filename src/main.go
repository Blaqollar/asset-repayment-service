package main

import (
	"asset-repayment-service/bootstrap"
	"go.uber.org/fx"
)

func main() {
	// fx.Run blocks until SIGINT/SIGTERM, then unwinds the lifecycle in
	// reverse: drain HTTP, close Redis, close the connection pool.
	fx.New(bootstrap.NewApp()).Run()
}
