FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cronwatch ./cmd/cronwatch

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cronwatch /usr/local/bin/cronwatch
ENTRYPOINT ["/usr/local/bin/cronwatch"]
CMD ["serve"]
