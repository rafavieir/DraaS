FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/draas-api ./cmd/draas-api && CGO_ENABLED=0 go build -trimpath -o /out/draas-worker ./cmd/draas-worker && CGO_ENABLED=0 go build -trimpath -o /out/draas-operator ./cmd/draas-operator && CGO_ENABLED=0 go build -trimpath -o /out/draasctl ./cmd/draasctl

FROM build AS test
RUN go vet ./... && go test -race ./... && go test ./internal/backup -run '^$' -bench . -benchmem

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/draas-api"]
