const GITHUB_URL = "https://github.com/turin-dev/rish-mcp";
const USAGE_URL = `${GITHUB_URL}/blob/master/docs/USAGE.md`;
const RELAY_URL = "https://rish-mcp.turin.my/relay";

function Arrow() {
  return <span aria-hidden="true">↗</span>;
}

function Signal({ tone = "orange" }: { tone?: "orange" | "green" | "blue" }) {
  return <span className={`signal ${tone}`} aria-hidden="true" />;
}

function ConnectionNode({
  index,
  label,
  detail,
  tone,
}: {
  index: string;
  label: string;
  detail: string;
  tone: "orange" | "green" | "blue";
}) {
  return (
    <div className={`connection-node ${tone}`}>
      <span className="node-index mono">{index}</span>
      <strong>{label}</strong>
      <small className="mono">{detail}</small>
    </div>
  );
}

const capabilities = [
  {
    number: "01",
    label: "REAL HARDWARE",
    title: "The phone in your hand.",
    body: "Read the battery, sensors, packages, logs, and OEM behavior that an emulator will always miss.",
  },
  {
    number: "02",
    label: "OUTBOUND ONLY",
    title: "One socket. No exposure.",
    body: "The Android agent dials your relay. No VPN, no open device port, and no inbound connection to maintain.",
  },
  {
    number: "03",
    label: "MCP NATIVE",
    title: "A tool your AI can call.",
    body: "Connect any compatible MCP client and use list_devices or run_shell like the rest of its tools.",
  },
];

const setupSteps = [
  {
    number: "01",
    title: "Run the relay",
    body: "Start the self-hosted control plane wherever you keep your services.",
    command: "curl -fsSL https://rish-mcp.turin.my/relay | sh",
  },
  {
    number: "02",
    title: "Pair the phone",
    body: "Use Android Wireless debugging, then let the agent connect outbound.",
    command: "rish-mcp agent  →  relay  →  phone",
  },
  {
    number: "03",
    title: "Give your AI a tool",
    body: "Add the relay's HTTP MCP endpoint and ask for the state of the real device.",
    command: "run_shell({ cmd: 'dumpsys battery' })",
  },
];

const faqs = [
  [
    "Does rish-mcp need root?",
    "No. Commands run as Android shell uid 2000 — the same privilege level as adb shell. Root-only operations remain unavailable.",
  ],
  [
    "Does the phone need an open port?",
    "No. The phone makes the outbound connection to your relay, so it can work behind CGNAT without exposing an inbound device port.",
  ],
  [
    "What does the first setup require?",
    "Android 11+ uses the phone's Wireless debugging pairing flow. Older devices use a one-time PC plus adb tcpip bridge.",
  ],
  [
    "Can I use it with my AI client?",
    "Yes. The relay exposes a standard HTTP MCP endpoint with run_shell and list_devices. Static bearer authentication and OAuth are supported.",
  ],
];

export default function Home() {
  return (
    <div className="site-shell">
      <header className="site-header">
        <div className="wrap header-inner">
          <a className="wordmark" href="#top">
            rish<span>-</span>mcp
          </a>
          <nav className="main-nav" aria-label="Main navigation">
            <a href="#features">Why rish</a>
            <a href="#how-it-works">How it works</a>
            <a href="#faq">FAQ</a>
          </nav>
          <a className="header-button" href={RELAY_URL} target="_blank" rel="noopener">
            Install relay <Arrow />
          </a>
        </div>
      </header>

      <main id="top">
        <section className="hero">
          <div className="wrap hero-grid">
            <div className="hero-copy">
              <p className="eyebrow eyebrow-light">
                <Signal /> ANDROID BRIDGE <span>/</span> OPEN SOURCE
              </p>
              <h1>
                Put your <em>phone</em>
                <br /> in the loop.
              </h1>
              <p className="hero-lede">
                rish-mcp gives your AI a direct, inspectable path to the Android
                device you actually use.
              </p>
              <div className="hero-actions">
                <a className="button button-accent" href={RELAY_URL} target="_blank" rel="noopener">
                  Install the relay <Arrow />
                </a>
                <a className="button button-dark" href={USAGE_URL} target="_blank" rel="noopener">
                  Read the docs
                </a>
              </div>
              <div className="hero-meta mono">
                <span><Signal tone="green" /> no root</span>
                <span><Signal tone="blue" /> no emulator</span>
                <span><Signal /> outbound only</span>
              </div>
            </div>

            <div className="hero-visual" aria-label="rish-mcp connection preview">
              <div className="connection-card">
                <div className="connection-topline mono">
                  <span>rish-mcp / live path</span>
                  <span className="live-label"><Signal tone="green" /> CONNECTED</span>
                </div>
                <div className="connection-route">
                  <ConnectionNode index="01" label="AI CLIENT" detail="HTTP / MCP" tone="orange" />
                  <div className="route-link mono"><span>tool call</span><i /></div>
                  <ConnectionNode index="02" label="YOUR RELAY" detail="WEBSOCKET" tone="green" />
                  <div className="route-link mono"><span>outbound</span><i /></div>
                  <ConnectionNode index="03" label="ANDROID" detail="UID 2000" tone="blue" />
                </div>
                <div className="terminal">
                  <div className="terminal-topline mono"><span>request stream</span><span>just now</span></div>
                  <div className="terminal-line mono"><span className="prompt">&gt;</span> dumpsys battery</div>
                  <div className="terminal-output mono"><span>level: 81</span><span>status: charging</span><span>temp: 29.4°C</span></div>
                </div>
                <div className="connection-footer mono">
                  <span><Signal tone="green" /> relay online</span>
                  <span>142 ms <b>↗ 18%</b></span>
                </div>
              </div>
              <div className="visual-note mono"><span>REAL DEVICE</span><strong>not a simulation</strong></div>
            </div>
          </div>
          <div className="hero-grid-lines" aria-hidden="true" />
        </section>

        <section className="proof-strip" aria-label="Product principles">
          <div className="wrap proof-grid">
            <div><span className="mono">01</span><strong>REAL DEVICE</strong><small>the hardware you own</small></div>
            <div><span className="mono">02</span><strong>UID 2000</strong><small>same ceiling as adb shell</small></div>
            <div><span className="mono">03</span><strong>NO INBOUND PORT</strong><small>phone dials the relay</small></div>
            <div><span className="mono">04</span><strong>GO + KOTLIN</strong><small>small enough to inspect</small></div>
          </div>
        </section>

        <section className="section features" id="features">
          <div className="wrap">
            <div className="section-intro">
              <div className="eyebrow">01 / why rish-mcp</div>
              <h2>Not another dashboard.<br /><em>A direct line to the device.</em></h2>
              <p>Useful context lives on the phone. rish-mcp keeps the path short, visible, and yours.</p>
            </div>
            <div className="capability-list">
              {capabilities.map((item) => (
                <article className="capability" key={item.number}>
                  <span className="capability-number mono">{item.number}</span>
                  <span className="capability-label mono">{item.label}</span>
                  <h3>{item.title}</h3>
                  <p>{item.body}</p>
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="section architecture" id="product">
          <div className="wrap">
            <div className="section-intro light-intro">
              <div className="eyebrow">02 / the product</div>
              <h2>Every hop is visible.<br /><em>Nothing is pretending.</em></h2>
              <p>One MCP endpoint, one outbound socket, one Android agent. The control plane stays understandable.</p>
            </div>
            <div className="architecture-grid">
              <div className="architecture-panel">
                <div className="panel-heading mono"><span>CONNECTION / 03 HOPS</span><span><Signal tone="green" /> HEALTHY</span></div>
                <div className="architecture-row">
                  <span className="row-number mono">01</span>
                  <div><strong>AI client</strong><small>standard HTTP MCP request</small></div>
                  <code>run_shell</code>
                </div>
                <div className="architecture-row">
                  <span className="row-number mono">02</span>
                  <div><strong>Self-hosted relay</strong><small>auth, routing, response timing</small></div>
                  <code>WebSocket</code>
                </div>
                <div className="architecture-row">
                  <span className="row-number mono">03</span>
                  <div><strong>Android agent</strong><small>your device, shell-level access</small></div>
                  <code>uid 2000</code>
                </div>
                <div className="panel-footer mono"><span>NO VPN REQUIRED</span><span>NO ROOT REQUIRED</span></div>
              </div>
              <aside className="architecture-aside">
                <span className="aside-mark">↳</span>
                <h3>Keep the private part private.</h3>
                <p>The relay holds the control plane. The public release server holds only version metadata and APK bytes — never your tokens or device data.</p>
                <a className="text-link text-link-light" href={GITHUB_URL} target="_blank" rel="noopener">Inspect the source <Arrow /></a>
              </aside>
            </div>
          </div>
        </section>

        <section className="section how" id="how-it-works">
          <div className="wrap">
            <div className="section-intro split-intro">
              <div>
                <div className="eyebrow">03 / how it works</div>
                <h2>From question<br /><em>to command.</em></h2>
              </div>
              <p>Three small steps take you from a clean install to useful answers from the device in your hand.</p>
            </div>
            <div className="setup-list">
              {setupSteps.map((step) => (
                <article className="setup-step" key={step.number}>
                  <span className="step-number mono">{step.number}</span>
                  <div className="step-copy"><h3>{step.title}</h3><p>{step.body}</p></div>
                  <code>{step.command}</code>
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="section faq" id="faq">
          <div className="wrap faq-grid">
            <div className="section-intro">
              <div className="eyebrow">04 / FAQ</div>
              <h2>Good to know<br /><em>before you start.</em></h2>
              <a className="text-link" href={USAGE_URL} target="_blank" rel="noopener">Read the full usage guide <Arrow /></a>
            </div>
            <div className="faq-list">
              {faqs.map(([question, answer], index) => (
                <details key={question} open={index === 0}>
                  <summary><span>{question}</span><b>+</b></summary>
                  <p>{answer}</p>
                </details>
              ))}
            </div>
          </div>
        </section>

        <section className="cta-section">
          <div className="wrap cta-panel">
            <div className="cta-copy">
              <div className="eyebrow eyebrow-light">READY WHEN YOU ARE</div>
              <h2>Put the phone<br /><em>back in the loop.</em></h2>
              <p>Build the relay. Install the agent. Ask your AI about what is actually there.</p>
            </div>
            <div className="install-block">
              <span className="mono">ONE COMMAND TO START</span>
              <code>curl -fsSL https://rish-mcp.turin.my/relay | sh</code>
              <a href={RELAY_URL} target="_blank" rel="noopener">Open installer <Arrow /></a>
            </div>
          </div>
        </section>
      </main>

      <footer className="site-footer">
        <div className="wrap footer-inner">
          <a className="wordmark" href="#top">rish<span>-</span>mcp</a>
          <span>MIT licensed · owner&apos;s own device only</span>
          <div><a href={USAGE_URL} target="_blank" rel="noopener">Docs</a><a href={GITHUB_URL} target="_blank" rel="noopener">GitHub <Arrow /></a></div>
        </div>
      </footer>
    </div>
  );
}
