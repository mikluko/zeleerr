FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags=-s -o /zelirr ./cmd/zelirr

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /zelirr /zelirr
EXPOSE 8080
ENTRYPOINT ["/zelirr"]
