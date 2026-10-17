module github.com/womat/manchester/demo/manchester_bench

go 1.27

require (
	github.com/womat/gpio v1.0.3
	github.com/womat/manchester v1.0.0
	golang.org/x/sys v0.49.0
)

require github.com/warthog618/go-gpiocdev v0.10.0 // indirect

replace github.com/womat/manchester => ../..
