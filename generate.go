//go:generate rm -vf autogen/gentemplates/gen.go
//go:generate rm -vf autogen/genstatic/gen.go
//go:generate mkdir -p static
//go:generate go run github.com/containous/go-bindata/go-bindata@latest -pkg gentemplates -nometadata -nocompress -o autogen/gentemplates/gen.go ./templates/...
//go:generate gofmt -s -w autogen/gentemplates/gen.go
//go:generate go run github.com/containous/go-bindata/go-bindata@latest -pkg genstatic -nocompress -o autogen/genstatic/gen.go ./static/...

package main

func main() {}
