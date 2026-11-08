package grpcserver

import (
	"bufio"
	"errors"
	"io"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// reflect serves grpc.reflection.v1 / v1alpha ServerReflectionInfo, so tools
// such as grpcurl and Postman can discover the mocked services without the
// .proto file.
func (c *call) reflect() {
	br := bufio.NewReader(c.r.Body)

	for {
		raw, err := c.readMessage(br)
		if errors.Is(err, io.EOF) {
			c.finish(usecase.GRPCOK, "")

			return
		}

		if err != nil {
			c.finish(usecase.GRPCInvalidArgument, err.Error())

			return
		}

		req, err := protobuf.DecodeReflectionRequest(raw)
		if err != nil {
			c.finish(usecase.GRPCInvalidArgument, err.Error())

			return
		}

		if c.send(c.reflectOne(req)) != nil {
			return
		}
	}
}

func (c *call) reflectOne(req *protobuf.ReflectionRequest) []byte {
	switch req.Kind {
	case protobuf.ReflectListServices:
		return protobuf.EncodeListServicesResponse(req, c.svc.ReflectServices(c.hint))
	case protobuf.ReflectFileByName:
		fds, gerr := c.svc.ReflectFile(c.hint, req.Arg)
		if gerr != nil {
			return protobuf.EncodeReflectionError(req, gerr.Code, gerr.Message)
		}

		return protobuf.EncodeFileDescriptorResponse(req, fds)
	case protobuf.ReflectFileBySymbol:
		fds, gerr := c.svc.ReflectSymbol(c.hint, req.Arg)
		if gerr != nil {
			return protobuf.EncodeReflectionError(req, gerr.Code, gerr.Message)
		}

		return protobuf.EncodeFileDescriptorResponse(req, fds)
	default:
		return protobuf.EncodeReflectionError(req, usecase.GRPCNotFound, "extensions are not supported")
	}
}
