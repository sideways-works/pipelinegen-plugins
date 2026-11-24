// Command s3-store is pipelinegen's store plugin for an S3 bucket.
package main

import (
	"os"

	"github.com/sideways-works/pipelinegen-plugins/store/s3/internal/store"
)

func main() {
	os.Exit(store.Serve(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
