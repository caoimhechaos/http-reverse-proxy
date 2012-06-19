env = Environment(ENV = {'GOROOT': '/usr/lib/go'}, TOOLS=['default', 'go', 'protoc'])

proto_files = env.Protoc([],
	"reverse-proxy-config.proto",
	PROTOCFLAGS='--plugin=protoc-gen-go=/usr/lib/go/bin/protoc-gen-go --go_out=.',
	PROTOCPYTHONOUTDIR='',
	)

proxy = env.Go('proxy', ['main.go', 'backend_connection.go',
						 'reverse-proxy-config.pb.go'], proto_files)
env.GoProgram('http-reverse-proxy', proxy)