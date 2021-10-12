package main

import (
	grpcda "github.com/celestiaorg/optimint/da/grpc"
	"github.com/celestiaorg/optimint/da/grpc/mockserv"
	"github.com/celestiaorg/optimint/store"
	"log"
	"net"
	"strconv"
)

func main() {
	var conf grpcda.Config
	// TODO(tzdybal): read config from somewhere
	conf = grpcda.DefaultConfig

	kv := store.NewKVStore(".", "db", "optimint")
	lis, err := net.Listen("tcp", conf.Host+":"+strconv.Itoa(conf.Port))
	if err != nil {
		log.Panic(err)
	}
	srv := mockserv.GetServer(kv, conf)
	if err := srv.Serve(lis); err != nil {
		log.Println("error while serving:", err)
	}
}
