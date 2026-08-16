"use client";

import { Fragment, useEffect, useRef, useState } from "react";

type NodeKey = "ai" | "relay" | "phone";

const CHAPTERS = [
  {
    step: 1,
    label: "path · 1 of 4",
    title: "AI asks",
    spec: "HTTPS · POST /mcp",
    body: (
      <>
        The AI sends one MCP call — {"run_shell({ cmd })"}. No session to manage: the relay builds a
        fresh MCP server for that single request.
      </>
    ),
    caption: "AI → relay",
  },
  {
    step: 2,
    label: "path · 2 of 4",
    title: "Relay dispatches",
    spec: "exec frame · WS",
    body: "The relay looks up the target device's already-open outbound socket and forwards the command with a fresh request id, so results can find their way back.",
    caption: "relay → phone",
  },
  {
    step: 3,
    label: "path · 3 of 4",
    title: "Phone executes",
    spec: "shell,v2 · 256 KB cap",
    body: (
      <>
        AdbShellClient runs it through the device&apos;s own adbd, as shell — the same ceiling as{" "}
        {"adb shell"}. Each output stream is capped; overflow is flagged, not dropped silently.
      </>
    ),
    caption: "executing on-device",
  },
  {
    step: 4,
    label: "path · 4 of 4",
    title: "Result returns",
    spec: "WS → HTTPS",
    body: "stdout, stderr, and the exit code ride back over the same socket, then straight back to the AI as the tool's response. The phone never had to open a port.",
    caption: "phone → relay → AI",
  },
];

const NODES: Array<{ key: NodeKey; title: string; sub: string }> = [
  { key: "ai", title: "AI client", sub: "Claude · MCP" },
  { key: "relay", title: "Go relay", sub: "MCP + WS" },
  { key: "phone", title: "Android", sub: "uid 2000 shell" },
];

function isNodeActive(step: number, key: NodeKey) {
  if (step === 1) return key === "ai";
  if (step === 2) return key === "relay";
  if (step === 3) return key === "phone";
  return key === "ai" || key === "relay"; // step 4: result rides back through both
}

function isLinkLit(step: number, linkIndex: 0 | 1) {
  return linkIndex === 0 ? step === 1 || step === 4 : step === 2 || step === 4;
}

function Icon({ nodeKey }: { nodeKey: NodeKey }) {
  if (nodeKey === "ai") {
    return (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" strokeLinecap="round">
        <path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3z" />
      </svg>
    );
  }
  if (nodeKey === "relay") {
    return (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round">
        <rect x="4" y="4" width="16" height="4.5" rx="1.1" />
        <rect x="4" y="9.75" width="16" height="4.5" rx="1.1" />
        <rect x="4" y="15.5" width="16" height="4.5" rx="1.1" />
        <circle cx="7.2" cy="6.25" r="0.6" fill="currentColor" stroke="none" />
        <circle cx="7.2" cy="12" r="0.6" fill="currentColor" stroke="none" />
        <circle cx="7.2" cy="17.75" r="0.6" fill="currentColor" stroke="none" />
      </svg>
    );
  }
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" strokeLinecap="round">
      <rect x="6.5" y="2.3" width="11" height="19.4" rx="2.2" />
      <line x1="10.3" y1="19" x2="13.7" y2="19" />
    </svg>
  );
}

export default function Journey() {
  const [active, setActive] = useState(1);
  const refs = useRef<Array<HTMLDivElement | null>>([]);
  const railRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let ticking = false;
    function update() {
      // Read line = vertical center of the pinned diagram itself, not a
      // fixed fraction of the viewport. That self-corrects for the sticky
      // offset, header height, and diagram size instead of needing a
      // hand-tuned padding constant that only holds at one viewport height.
      const rail = railRef.current;
      if (!rail) return;
      const railRect = rail.getBoundingClientRect();
      const readingLine = railRect.top + railRect.height / 2;
      const distances = refs.current.map((el) => {
        if (!el) return Infinity;
        const rect = el.getBoundingClientRect();
        return Math.abs(rect.top + rect.height / 2 - readingLine);
      });
      // Hysteresis: only switch away from the current step once another one
      // is clearly closer, not the instant it's nearest by a hair. Holds
      // each step steady near the boundary, then hands off once the reader
      // has scrolled clearly past it, instead of flip-flopping.
      const HYSTERESIS = 70;
      setActive((prev) => {
        let bestIdx = prev - 1;
        let bestDist = distances[prev - 1] ?? Infinity;
        distances.forEach((d, i) => {
          if (d < bestDist - HYSTERESIS) {
            bestDist = d;
            bestIdx = i;
          }
        });
        return bestIdx + 1;
      });
    }
    function onScroll() {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(() => {
        update();
        ticking = false;
      });
    }
    update();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
    };
  }, []);

  const current = CHAPTERS[active - 1];

  return (
    <div className="journey-grid">
      <div className="diagram-rail" ref={railRef}>
        <div
          className="flat-diagram"
          role="img"
          aria-label="Three connected modules — AI client, Go relay, Android agent — with the active connection highlighted as the reader scrolls through the request path."
        >
          {NODES.map((n, i) => (
            <Fragment key={n.key}>
              <div className="flat-node" data-active={isNodeActive(active, n.key)}>
                <span className="flat-icon">
                  <Icon nodeKey={n.key} />
                </span>
                <span className="flat-title">{n.title}</span>
                <span className="flat-sub mono">{n.sub}</span>
              </div>
              {i < NODES.length - 1 && <div className="flat-link" data-lit={isLinkLit(active, i as 0 | 1)} />}
            </Fragment>
          ))}
        </div>
        <p className="caption-row">
          <span>{current.caption}</span>
          <span>device never accepts inbound</span>
        </p>
        <span className="step-count">step {active} / 4</span>
      </div>

      <div className="chapters">
        {CHAPTERS.map((c, i) => (
          <div
            key={c.step}
            ref={(el) => {
              refs.current[i] = el;
            }}
            className={`chapter${active === c.step ? " active" : ""}`}
            data-step={c.step}
          >
            <p className="step">{c.label}</p>
            <div className="chapter-head">
              <h3>{c.title}</h3>
              <span className="spec mono">{c.spec}</span>
            </div>
            <p>{c.body}</p>
          </div>
        ))}
      </div>
    </div>
  );
}
