env = Environment(TOOLS=['default', 'go'])

proxy = env.Go('proxy', ['main.go', 'backend_connection.go',
						 'reverse-proxy-config.pb.go'])
env.GoProgram('http-reverse-proxy', proxy)
