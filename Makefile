VERSION=$(shell git describe --abbrev=0 --tags)
# BUILD=$(shell git rev-parse HEAD)
DIRBUILD=./build
# DIR=${DIRBUILD}/${VERSION}/${BUILD}/bin
DIR=${DIRBUILD}

# LDFLAGS=-ldflags "-s -w ${XBUILD} -buildid=${BUILD} -X github.com/VHSgunzo/chisel/share.BuildVersion=${VERSION}"
LDFLAGS=-ldflags "-s -w ${XBUILD} -buildid= -X github.com/VHSgunzo/chisel/share.BuildVersion=${VERSION}"

GOFILES=`go list ./...`
GOFILESNOTEST=`go list ./... | grep -v test`

# Make Directory to store executables
$(shell mkdir -p ${DIR})

all:
	@goreleaser build --skip-validate --single-target --config .github/goreleaser.yml

freebsd: lint
	env CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -trimpath ${LDFLAGS} ${GCFLAGS} ${ASMFLAGS} -o ${DIR}/chisel-freebsd_amd64 .

linux-x86_64: lint
	env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ${LDFLAGS} ${GCFLAGS} ${ASMFLAGS} -o ${DIR}/chisel-linux-x86_64 .
linux-aarch64: lint
	env CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath ${LDFLAGS} ${GCFLAGS} ${ASMFLAGS} -o ${DIR}/chisel-linux-aarch64 .

windows: lint
	env CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -trimpath ${LDFLAGS} ${GCFLAGS} ${ASMFLAGS} -o ${DIR}/chisel-windows_amd64 .

darwin:
	env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath ${LDFLAGS} ${GCFLAGS} ${ASMFLAGS} -o ${DIR}/chisel-darwin_amd64 .

docker:
	@docker build .

dep: ## Get the dependencies
	@go get -u github.com/goreleaser/goreleaser
	@go get -u github.com/boumenot/gocover-cobertura
	@go get -v -d ./...
	@go get -u all
	@go mod tidy

lint: ## Lint the files
	@go fmt ${GOFILES}
	@go vet ${GOFILESNOTEST}

test: ## Run unit tests
	@go test -coverprofile=${DIR}/coverage.out -race -short ${GOFILESNOTEST}
	@go tool cover -html=${DIR}/coverage.out -o ${DIR}/coverage.html
	@gocover-cobertura < ${DIR}/coverage.out > ${DIR}/coverage.xml

release: lint test
	goreleaser release --config .github/goreleaser.yml

clean:
	rm -rf ${DIRBUILD}/*

.PHONY: all freebsd linux windows docker dep lint test release clean
