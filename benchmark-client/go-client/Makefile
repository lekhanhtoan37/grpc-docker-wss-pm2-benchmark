.PHONY: proto build clean

proto:
	protoc --go_out=. --go_opt=module=benchmark-client \
	       --go-grpc_out=. --go-grpc_opt=module=benchmark-client \
	       proto/control.proto

build:
	go build -o benchmark-client .

build-coordinator:
	go build -o coordinator ./cmd/coordinator/

build-worker:
	go build -o worker ./cmd/worker/

clean:
	rm -f benchmark-client coordinator worker
