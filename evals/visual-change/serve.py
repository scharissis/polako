# Serves this directory on loopback and says where, straight away.
import http.server
import socketserver


class Server(http.server.ThreadingHTTPServer):
    def server_bind(self):
        # HTTPServer.server_bind looks the host's name up first, which can take
        # half a minute on a Mac; a dev server has no use for the name.
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = "localhost", self.server_address[1]


with Server(("127.0.0.1", 4173), http.server.SimpleHTTPRequestHandler) as srv:
    print(f"serving http://127.0.0.1:{srv.server_address[1]}/", flush=True)
    srv.serve_forever()
