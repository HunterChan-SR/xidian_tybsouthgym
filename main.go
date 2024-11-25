package main

import (
	xidianTybsouthgymClient "xidian_tybsouthgym/client"
	"xidian_tybsouthgym/server"
)

const Domain = "tybsouthgym.xidian.edu.cn"
const HostUrl = "https://" + Domain + "/"

func WithCli() {
	xdgym := xidianTybsouthgymClient.DefaultClient()

	xdgym.GetOrderByTime()
}
func WithWeb() {
	server.Run()
}

func main() {
	// WithCli()
	WithWeb()
}
