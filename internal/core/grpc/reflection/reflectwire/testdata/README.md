# Reflection message goldens

Each `.bin` file is one `ServerReflectionRequest` (`req_*`) or
`ServerReflectionResponse` (`resp_*`) encoded by grpc-go, so the hand-written
codec is compared with the generated one. Nothing here is shipped.

They were written on 2026-09-16 by a throwaway Go program outside this
repository, in its own module, using `google.golang.org/grpc` v1.78.0
(`reflection/grpc_reflection_v1` and `grpc_reflection_v1alpha`) and
`google.golang.org/protobuf` v1.36.11 with Go 1.27.0, encoding with
`proto.MarshalOptions{Deterministic: true}`. PostSonde does not depend on
either module. The program built every message with both the v1 and the
v1alpha types and required identical bytes, so the files stand for both
protocol versions. The values are listed in `wire_test.go`.
