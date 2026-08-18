import Journey from "@/components/Journey";

export default function Home() {
  return (
    <>
      <header className="site">
        <div className="wrap">
          <div className="wordmark">
            rish<span className="dot">-</span>mcp
          </div>
          <nav className="site">
            <a href="https://github.com/turin-dev/rish-mcp" target="_blank" rel="noopener">
              GitHub
            </a>
          </nav>
        </div>
      </header>

      <main>
        <section className="hero">
          <div className="wrap">
            <p className="kicker">
              <span className="led" />
              rewrite in progress · rev 0.1
            </p>
            <h1>
              Give your AI <em>a phone</em> to work with.
            </h1>
            <p className="lede">
              rish-mcp exposes a real Android device&apos;s shell — <code>adb</code>-level, uid 2000 — to
              any MCP client. No emulator standing in for the real thing. No VPN, no open port on the
              device. Just an agent that dials out.
            </p>
            <div className="cta-row">
              <a className="btn primary" href="https://github.com/turin-dev/rish-mcp" target="_blank" rel="noopener">
                Read the source
              </a>
              <a
                className="btn ghost"
                href="https://github.com/turin-dev/rish-mcp/blob/master/docs/USAGE.md"
                target="_blank"
                rel="noopener"
              >
                Usage guide
              </a>
            </div>
          </div>
        </section>

        <section className="journey">
          <div className="wrap">
            <div className="journey-head">
              <p className="eyebrow">how a command travels</p>
              <h2>One outbound socket. Four hops, round trip.</h2>
            </div>
            <Journey />
          </div>
        </section>

        <section className="example">
          <div className="wrap">
            <div className="section-head">
              <p className="eyebrow">in practice</p>
              <h2>Ask in plain language. It runs on the phone.</h2>
            </div>
            <div className="terminal mono">
              <div className="bar">
                <span />
                <span />
                <span />
              </div>
              <pre>
                <span className="prompt">you</span>
                {"    Is my phone's battery okay?\n"}
                <span className="prompt">claude</span> <span className="call">{"run_shell({ cmd: \"dumpsys battery\" })"}</span>
                {"\n"}
                <span className="out">
                  {"        exit=0 (142ms)\n        --- stdout ---\n        level: 81, status: charging, temperature: 29.4°C"}
                </span>
                {"\n"}
                <span className="prompt">claude</span> Charging at 81%, running cool at 29°C — all good.
              </pre>
            </div>
          </div>
        </section>

        <section className="why">
          <div className="wrap">
            <div className="section-head">
              <p className="eyebrow">why this exists</p>
              <h2>The gap between an AI and your actual device.</h2>
            </div>
            <div className="why-grid">
              <div className="why-card">
                <h3>Emulators lie.</h3>
                <p>
                  Assistants that develop against a virtual device never see the sensors, the battery
                  quirks, or the OEM behavior of the phone in your pocket.
                </p>
              </div>
              <div className="why-card">
                <h3>&quot;Just run adb&quot; isn&apos;t help.</h3>
                <p>
                  When something breaks on-device, the usual answer is a shell command typed by hand.
                  rish-mcp lets the AI run that command itself.
                </p>
              </div>
              <div className="why-card">
                <h3>No app to trust blindly.</h3>
                <p>
                  The previous build needed Shizuku — a separate app, a separate grant. This one pairs
                  with the device&apos;s own <code className="mono">adbd</code> directly.
                </p>
              </div>
            </div>
          </div>
        </section>

        <section className="components">
          <div className="wrap">
            <div className="section-head">
              <p className="eyebrow">what&apos;s in the box</p>
              <h2>Two Go binaries, one Android agent.</h2>
            </div>
            <div className="comp-grid">
              <a
                className="comp-card"
                href="https://github.com/turin-dev/rish-mcp/tree/master/server/cmd/relay"
                target="_blank"
                rel="noopener"
              >
                <span className="tag mono">server/cmd/relay</span>
                <h3>Relay + MCP server</h3>
                <p>
                  Streamable-HTTP MCP endpoint (<code className="mono">run_shell</code>,{" "}
                  <code className="mono">list_devices</code>) and the WebSocket the agent dials into.
                  Static bearer or OAuth, your call.
                </p>
                <span className="more">Browse the source →</span>
              </a>
              <a className="comp-card" href="https://github.com/turin-dev/rish-mcp/tree/master/app" target="_blank" rel="noopener">
                <span className="tag mono">app/</span>
                <h3>Android agent</h3>
                <p>
                  Pairs with wireless debugging on Android 11+, or a PC + <code className="mono">adb tcpip</code>{" "}
                  bridge below that. No third-party grant screen.
                </p>
                <span className="more">Browse the source →</span>
              </a>
              <a
                className="comp-card"
                href="https://github.com/turin-dev/rish-mcp/tree/master/server/internal/oauth"
                target="_blank"
                rel="noopener"
              >
                <span className="tag mono">internal/oauth</span>
                <h3>OAuth layer</h3>
                <p>
                  Stateless, HMAC-signed off your own token — no database. Rotate the token, every issued
                  credential dies with it.
                </p>
                <span className="more">Browse the source →</span>
              </a>
              <a
                className="comp-card"
                href="https://github.com/turin-dev/rish-mcp/tree/master/server/cmd/publicserver"
                target="_blank"
                rel="noopener"
              >
                <span className="tag mono">server/cmd/publicserver</span>
                <h3>Version server</h3>
                <p>A second, secret-free binary that only answers &quot;what build is current&quot; and hands out the APK. No path to the relay.</p>
                <span className="more">Browse the source →</span>
              </a>
            </div>
          </div>
        </section>

        <section className="quickstart">
          <div className="wrap">
            <div className="section-head">
              <p className="eyebrow">quick start</p>
              <h2>Two binaries, one build.</h2>
            </div>
            <pre className="code-block mono">
              <span className="rem"># server (both binaries)</span>
              {"\n"}
              <span className="kw">cd</span> server && go build ./... && go test ./...
              {"\n\n"}
              <span className="rem"># or as containers</span>
              {"\n"}
              docker build --target relay -t rishmcp-relay server
              {"\n"}
              docker build --target publicserver -t rishmcp-public server
              {"\n\n"}
              <span className="rem"># Android APK — Android SDK + Gradle run inside Docker, host stays clean</span>
              {"\n"}
              <span className="kw">cd</span> app && docker build -t rishmcp-android-build -f Dockerfile.build .
            </pre>
          </div>
        </section>

        <section className="status">
          <div className="wrap">
            <div className="section-head">
              <p className="eyebrow">build log</p>
              <h2>What&apos;s actually running.</h2>
            </div>
            <table className="status-table">
              <thead>
                <tr>
                  <th>Rev</th>
                  <th>Component</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td className="rev">0.1</td>
                  <td>Go relay — MCP tools, WS relay</td>
                  <td>
                    <span className="pill ok">built · tested</span>
                  </td>
                </tr>
                <tr>
                  <td className="rev">0.1</td>
                  <td>Android agent — ADB pairing, shell exec</td>
                  <td>
                    <span className="pill ok">built · tested</span>
                  </td>
                </tr>
                <tr>
                  <td className="rev">0.1</td>
                  <td>OAuth layer</td>
                  <td>
                    <span className="pill ok">built · tested</span>
                  </td>
                </tr>
                <tr>
                  <td className="rev">0.1</td>
                  <td>Official version server</td>
                  <td>
                    <span className="pill ok">built · tested</span>
                  </td>
                </tr>
                <tr>
                  <td className="rev">—</td>
                  <td>Low-spec wake path (FCM)</td>
                  <td>
                    <span className="pill pending">blocked · needs Firebase project</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </main>

      <footer className="site">
        <div className="wrap">
          <span>MIT licensed · owner&apos;s own device only</span>
          <span>
            <a href="https://github.com/turin-dev/rish-mcp" target="_blank" rel="noopener">
              github.com/turin-dev/rish-mcp
            </a>
          </span>
        </div>
      </footer>
    </>
  );
}
