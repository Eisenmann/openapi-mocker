package protobuf

import "fmt"

// Reflection request kinds (the oneof of grpc.reflection.v1.ServerReflectionRequest).
const (
	ReflectFileByName   = "file_by_filename"
	ReflectFileBySymbol = "file_containing_symbol"
	ReflectListServices = "list_services"
	ReflectExtension    = "extension"
)

// ReflectionRequest is a decoded ServerReflectionRequest.
type ReflectionRequest struct {
	Host string
	Kind string
	// Arg is the file name or symbol of the request.
	Arg string
	// Raw is the request as received (echoed in the response).
	Raw []byte
}

// DecodeReflectionRequest parses a ServerReflectionRequest message.
func DecodeReflectionRequest(b []byte) (*ReflectionRequest, error) {
	fields, err := readFields(b)
	if err != nil {
		return nil, err
	}

	req := &ReflectionRequest{Raw: b} //nolint:exhaustruct,exhaustruct_v5 // omitted fields are zero

	for _, f := range fields {
		switch f.num {
		case 1:
			req.Host = string(f.data)
		case 3: //nolint:mnd // file_by_filename.
			req.Kind, req.Arg = ReflectFileByName, string(f.data)
		case 4: //nolint:mnd // file_containing_symbol.
			req.Kind, req.Arg = ReflectFileBySymbol, string(f.data)
		case 5, 6: //nolint:mnd // extension queries.
			req.Kind = ReflectExtension
		case 7: //nolint:mnd // list_services.
			req.Kind, req.Arg = ReflectListServices, string(f.data)
		}
	}

	if req.Kind == "" {
		return nil, fmt.Errorf("%w: empty reflection request", ErrWire)
	}

	return req, nil
}

func reflectionEnvelope(req *ReflectionRequest) []byte {
	out := appendString(nil, 1, req.Host)

	return appendBytes(appendTag(out, 2, wireBytes), req.Raw) //nolint:mnd // original_request.
}

// EncodeFileDescriptorResponse builds a ServerReflectionResponse that carries file descriptors.
func EncodeFileDescriptorResponse(req *ReflectionRequest, descriptors [][]byte) []byte {
	var inner []byte
	for _, d := range descriptors {
		inner = appendBytes(appendTag(inner, 1, wireBytes), d)
	}

	return appendBytes(appendTag(reflectionEnvelope(req), 4, wireBytes), inner) //nolint:mnd // file_descriptor_response.
}

// EncodeListServicesResponse builds a ServerReflectionResponse that lists services.
func EncodeListServicesResponse(req *ReflectionRequest, names []string) []byte {
	var inner []byte

	for _, n := range names {
		svc := appendString(nil, 1, n)
		inner = appendBytes(appendTag(inner, 1, wireBytes), svc)
	}

	return appendBytes(appendTag(reflectionEnvelope(req), 6, wireBytes), inner) //nolint:mnd // list_services_response.
}

// EncodeReflectionError builds a ServerReflectionResponse that carries an error.
func EncodeReflectionError(req *ReflectionRequest, code int, message string) []byte {
	inner := appendUint(nil, 1, uint64(code)) //nolint:gosec // gRPC codes are small and non-negative.
	inner = appendString(inner, 2, message)   //nolint:mnd // error_message.

	return appendBytes(appendTag(reflectionEnvelope(req), 7, wireBytes), inner) //nolint:mnd // error_response.
}
