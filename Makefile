.PHONY: controller agent nodes fabric-status test vet race version build release-assets clean

controller:
	go run ./cmd/nibia-controller --pair-listen 0.0.0.0:8080 --secure-listen 0.0.0.0:8443 --relay-listen 0.0.0.0:9443

agent:
	go run ./cmd/nibia-agent

nodes:
	go run ./cmd/nibia nodes --controller http://127.0.0.1:8080

fabric-status:
	go run ./cmd/nibia fabric status --controller http://127.0.0.1:8080

test:
	go test ./...

vet:
	go vet ./...

race:
	go test -race ./...

version:
	go run ./cmd/nibia version

build:
	mkdir -p bin
	go build -trimpath -buildvcs=false -o bin/nibia-controller ./cmd/nibia-controller
	go build -trimpath -buildvcs=false -o bin/nibia-agent ./cmd/nibia-agent
	go build -trimpath -buildvcs=false -o bin/nibia ./cmd/nibia

release-assets:
	./packaging/build-release.sh

clean:
	rm -rf bin
