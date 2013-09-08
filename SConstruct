env = Environment(ENV = {'GOROOT': '/usr/lib/go'}, TOOLS=['default', 'go', 'protoc'])

proto_files = env.Protoc([],
	"reverse-proxy-config.proto",
	PROTOCFLAGS='--plugin=protoc-gen-go=/usr/bin/protoc-gen-go --go_out=.',
	PROTOCPYTHONOUTDIR='',
	)

proxy = env.Go('proxy', ['main.go', 'backend_connection.go',
			 'mutex.go', 'reverse-proxy-config.pb.go'],
			 proto_files)
prog = env.GoProgram('http-reverse-proxy', proxy)
env.Install("/usr/local/bin", prog)
env.Alias('install', ['/usr/local/bin'])

env.Clean('protobufs', ['reverse-proxy-config.pb.go'])
