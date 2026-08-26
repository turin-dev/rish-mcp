const GITHUB_URL = "https://github.com/turin-dev/rish-mcp";
const USAGE_URL = `${GITHUB_URL}/blob/master/docs/USAGE.md`;

function Arrow() {
  return <span aria-hidden="true">↗</span>;
}

function StatusDot({ color = "indigo" }: { color?: "indigo" | "green" | "orange" }) {
  return <span className={`status-dot ${color}`} aria-hidden="true" />;
}

function DevicePreview() {
  return (
    <div className="device-preview" aria-label="rish-mcp device dashboard preview">
      <div className="preview-sidebar">
        <div className="preview-brand">rish<span>-</span>mcp</div>
        <div className="preview-nav active"><span>⌂</span> Overview</div>
        <div className="preview-nav"><span>⌁</span> Commands</div>
        <div className="preview-nav"><span>◌</span> Devices</div>
        <div className="preview-nav"><span>⚙</span> Settings</div>
        <div className="preview-side-bottom"><StatusDot color="green" /> relay online</div>
      </div>
      <div className="preview-main">
        <div className="preview-topbar"><span className="preview-breadcrumb">Overview</span><span className="preview-avatar">AI</span></div>
        <div className="preview-heading"><div><div className="preview-kicker mono">YOUR DEVICE / 01</div><h3>Good morning, AI.</h3><p>Everything is connected and ready to run.</p></div><span className="online-chip"><StatusDot color="green" /> connected</span></div>
        <div className="preview-stats">
          <div><span className="preview-stat-label">DEVICE</span><strong>Pixel 8 Pro</strong><small>Android 15 · shell</small></div>
          <div><span className="preview-stat-label">RELAY LATENCY</span><strong>142 ms</strong><small><span className="positive">↓ 18%</span> this week</small></div>
        </div>
        <div className="preview-command"><div className="command-heading"><span>Recent command</span><span className="mono">just now</span></div><div className="command-box mono"><span className="command-prompt">$</span> dumpsys battery <span className="command-check">✓</span></div><div className="command-output mono"><span>level: 81</span><span>status: charging</span><span>temperature: 29.4°C</span></div></div>
      </div>
    </div>
  );
}

const featureCards = [
  { icon: "◎", title: "Real hardware", body: "Work with the sensors, battery, OEM behavior, and Android build that actually matter." },
  { icon: "↗", title: "Outbound by default", body: "Your phone dials the relay. No VPN, no open port, and no inbound connection to expose." },
  { icon: "⌘", title: "MCP native", body: "Connect any compatible AI client and call run_shell or list_devices like regular tools." },
  { icon: "▣", title: "Shell-level access", body: "Commands run at uid 2000, the same predictable ceiling as adb shell. No root required." },
  { icon: "◌", title: "Self-hosted relay", body: "Keep the private control plane on infrastructure you own, with static bearer or OAuth." },
  { icon: "⌁", title: "Small and focused", body: "A Go relay and one Android agent keep the path clear, inspectable, and easy to debug." },
];

const productCards = [
  { label: "COMMAND CENTER", title: "Ask in plain language.", body: "Your AI turns a question into a shell command, then brings the result back with exit code and timing.", className: "product-command" },
  { label: "DEVICE STATUS", title: "See what is really happening.", body: "Battery, network, package state, logs, and sensors — from the phone in your hand, not an emulator.", className: "product-device" },
  { label: "SAFE CONNECTION", title: "One socket. No exposure.", body: "The Android agent keeps an outbound WebSocket to your relay and never listens for the internet.", className: "product-network" },
  { label: "OPEN SOURCE", title: "Follow every hop.", body: "Go, Kotlin, and documented wire frames make the whole path available for inspection.", className: "product-source" },
];

const faqs = [
  ["Does rish-mcp need root?", "No. Commands run as Android shell uid 2000 — the same privilege level as adb shell. Root-only operations still remain unavailable."],
  ["Does the phone need an open port?", "No. The phone always makes the outbound connection to your relay, so it can work behind CGNAT without exposing an inbound device port."],
  ["What does the first setup require?", "Android 11+ uses the phone's Wireless debugging pairing flow. Older devices use a one-time PC plus adb tcpip bridge."],
  ["Can I use it with my AI client?", "Yes. The relay exposes a standard HTTP MCP endpoint with run_shell and list_devices. Static bearer authentication and OAuth are supported."],
];

export default function Home() {
  return (
    <div className="site-shell">
      <header className="site-header">
        <div className="wrap header-inner">
          <a className="wordmark" href="#top">rish<span>-</span>mcp</a>
          <nav className="main-nav" aria-label="Main navigation">
            <a href="#features">Features</a>
            <a href="#product">Product</a>
            <a href="#faq">FAQ</a>
          </nav>
          <a className="header-button" href={USAGE_URL} target="_blank" rel="noopener">Get started <Arrow /></a>
        </div>
      </header>

      <main id="top">
        <section className="hero">
          <div className="wrap hero-grid">
            <div className="hero-copy">
              <div className="beta-badge"><StatusDot /> open source · public beta</div>
              <h1>Your AI,<br /><em>with a real device.</em></h1>
              <p>rish-mcp connects any MCP client to the Android phone you actually use. Inspect it, debug it, and let your AI work with the real thing.</p>
              <div className="hero-actions"><a className="button primary" href={USAGE_URL} target="_blank" rel="noopener">Start building <Arrow /></a><a className="button secondary" href={GITHUB_URL} target="_blank" rel="noopener">View the source</a></div>
              <div className="hero-trust mono"><span>✓</span> no emulator &nbsp;·&nbsp; no VPN &nbsp;·&nbsp; no root</div>
            </div>
            <div className="hero-visual"><DevicePreview /><div className="floating-note note-one"><StatusDot color="green" /><span><strong>142 ms</strong><small>relay response</small></span></div><div className="floating-note note-two mono">uid 2000 <span>●</span></div></div>
          </div>
          <div className="hero-orbit orbit-one" /><div className="hero-orbit orbit-two" />
        </section>

        <section className="proof-strip"><div className="wrap proof-grid"><div><strong>REAL DEVICE</strong><span>Not an emulator</span></div><div><strong>UID 2000</strong><span>Same as adb shell</span></div><div><strong>OUTBOUND ONLY</strong><span>No port exposed</span></div><div><strong>OPEN SOURCE</strong><span>Go + Kotlin</span></div></div></section>

        <section className="features section" id="features"><div className="wrap"><div className="section-heading centered"><div className="eyebrow">01 / features</div><h2>Everything your AI needs<br /><em>from your actual phone.</em></h2><p>From shell commands to device state, rish-mcp keeps the useful parts close to the hardware.</p></div><div className="feature-grid">{featureCards.map((feature) => <article className="feature-card" key={feature.title}><span className="feature-icon">{feature.icon}</span><h3>{feature.title}</h3><p>{feature.body}</p><span className="feature-more">Learn more <Arrow /></span></article>)}</div></div></section>

        <section className="product section" id="product"><div className="wrap"><div className="section-heading centered"><div className="eyebrow">02 / product</div><h2>The real device,<br /><em>in one clear view.</em></h2><p>A small control plane for the commands, connection, and hardware that your AI needs to see.</p></div><div className="product-grid">{productCards.map((card) => <article className={`product-card ${card.className}`} key={card.title}><div className="product-card-copy"><span className="product-label mono">{card.label}</span><h3>{card.title}</h3><p>{card.body}</p></div><div className="product-art">{card.className === "product-command" && <><div className="art-terminal mono"><span className="art-muted">you</span> check my battery{`\n`}<span className="art-accent">run_shell({`{ cmd: 'dumpsys battery' }`})</span>{`\n`}<span className="art-green">exit=0&nbsp;&nbsp;level: 81&nbsp;&nbsp;charging</span></div></>}{card.className === "product-device" && <div className="art-device-card"><div className="art-device-icon">▯</div><div><strong>Pixel 8 Pro</strong><span>Android 15</span></div><StatusDot color="green" /></div>}{card.className === "product-network" && <div className="art-network"><span>AI</span><i /><span>relay</span><i /><span>phone</span></div>}{card.className === "product-source" && <div className="art-source mono"><span>go</span><span>kotlin</span><span>mcp</span><span>ws</span></div>}</div></article>)}</div></div></section>

        <section className="how section"><div className="wrap"><div className="section-heading"><div className="eyebrow">03 / how it works</div><h2>From question to<br /><em>command in seconds.</em></h2></div><div className="steps-grid"><article><span className="step-number">01</span><h3>Connect your device</h3><p>Pair the app with adbd, then let the Android agent dial your self-hosted relay.</p><span className="step-line" /></article><article><span className="step-number">02</span><h3>Connect your AI</h3><p>Add the relay&apos;s HTTP MCP endpoint to Claude or any compatible MCP client.</p><span className="step-line" /></article><article><span className="step-number">03</span><h3>Ask for the real thing</h3><p>Your AI calls the device, receives stdout and stderr, and explains what happened.</p><span className="step-line" /></article></div></div></section>

        <section className="faq section" id="faq"><div className="wrap faq-grid"><div className="section-heading"><div className="eyebrow">04 / FAQ</div><h2>Good to know<br /><em>before you start.</em></h2><a className="text-link" href={USAGE_URL} target="_blank" rel="noopener">Read the full usage guide <Arrow /></a></div><div className="faq-list">{faqs.map(([question, answer], index) => <details key={question} open={index === 0}><summary><span>{question}</span><b>+</b></summary><p>{answer}</p></details>)}</div></div></section>

        <section className="cta-section"><div className="wrap cta-panel"><div className="eyebrow">ready when you are</div><h2>Make your AI<br /><em>device-aware.</em></h2><p>Build the relay. Install the agent. Put the phone in the loop.</p><div className="hero-actions"><a className="button light" href={USAGE_URL} target="_blank" rel="noopener">Get started <Arrow /></a><a className="button outline-light" href={GITHUB_URL} target="_blank" rel="noopener">GitHub <Arrow /></a></div></div></section>
      </main>

      <footer className="site-footer"><div className="wrap footer-inner"><a className="wordmark" href="#top">rish<span>-</span>mcp</a><span>MIT licensed · owner&apos;s own device only</span><div><a href={USAGE_URL} target="_blank" rel="noopener">Docs</a><a href={GITHUB_URL} target="_blank" rel="noopener">GitHub <Arrow /></a></div></div></footer>
    </div>
  );
}
