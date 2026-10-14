FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aether ./cmd/aether

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aether /usr/local/bin/aether
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --retries=5 CMD ["/usr/local/bin/aether", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/aether"]
CMD ["serve"]
