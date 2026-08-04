/*
 * Guardian dashboard UI — vanilla JS, no build step (dashboard plan §2: a new
 * build target would need its own argued case, and ~15 read-only panels do not
 * warrant one).
 *
 * Every panel renders from a JSON snapshot polled from this same listener. Two
 * rules run throughout:
 *
 *  - An unavailable section says WHY. Rendering zeros for an unreachable node
 *    is the one failure mode that could cost an operator money: a zeroed float
 *    panel and a dead endpoint look identical otherwise, and one is an
 *    emergency.
 *  - Nothing is invented. Where the daemon cannot know something (typical bond
 *    size before anything has bonded), the panel omits it rather than guessing.
 */
'use strict';

const POLL_MS = 7000;

/* ---------- small helpers ---------- */

const $ = (id) => document.getElementById(id);

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined && text !== null) n.textContent = String(text);
  return n;
}

/** Key/value rows. Values are set as text, never HTML — chain data is untrusted. */
function kvList(pairs) {
  const dl = el('dl');
  for (const [k, v, cls] of pairs) {
    if (v === undefined || v === null || v === '') continue;
    const row = el('div', 'kv');
    row.append(el('dt', null, k));
    row.append(el('dd', cls || null, v));
    dl.append(row);
  }
  return dl;
}

function table(headers, rows) {
  const wrap = el('div', 'scroll');
  const t = el('table');
  const thead = el('thead');
  const htr = el('tr');
  headers.forEach((h) => htr.append(el('th', null, h)));
  thead.append(htr);
  t.append(thead);
  const tb = el('tbody');
  rows.forEach((cells) => {
    const tr = el('tr');
    cells.forEach((c) => {
      if (c && typeof c === 'object' && 'node' in c) {
        const td = el('td');
        td.append(c.node);
        tr.append(td);
      } else {
        tr.append(el('td', null, c === undefined || c === null ? '—' : c));
      }
    });
    tb.append(tr);
  });
  t.append(tb);
  wrap.append(t);
  return wrap;
}

function chip(text, kind) {
  return el('span', 'chip' + (kind ? ' ' + kind : ''), text);
}

/** uveil → VEIL, 6dp, thousands-grouped. BigInt so large pools stay exact. */
function veil(uveil) {
  if (uveil === undefined || uveil === null || uveil === '') return '—';
  let n;
  try {
    n = BigInt(String(uveil).replace(/[^\d-]/g, '') || '0');
  } catch {
    return String(uveil);
  }
  const neg = n < 0n;
  if (neg) n = -n;
  const whole = n / 1000000n;
  const frac = (n % 1000000n).toString().padStart(6, '0').replace(/0+$/, '');
  const grouped = whole.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return (neg ? '-' : '') + grouped + (frac ? '.' + frac : '') + ' VEIL';
}

function blocks(n) {
  if (n === undefined || n === null) return '—';
  if (n < 0) return `passed (${Math.abs(n)} ago)`;
  return `${n} blocks`;
}

function when(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '—';
  return d.toLocaleString('en-GB');
}

/** Renders an unavailable section and returns true if it was unavailable. */
function handleUnavailable(node, data) {
  if (!data || !data.unavailable) return false;
  node.replaceChildren();
  const w = el('p', 'warn', data.reason
    ? `Unavailable — ${data.reason}`
    : 'Unavailable. The daemon could not assemble this section.');
  node.append(w);
  return true;
}

function setEmpty(node, msg) {
  node.replaceChildren(el('p', 'empty', msg));
}

/* ---------- panels ---------- */

function renderVitals(d) {
  const body = $('vitals-body');
  if (handleUnavailable(body, d)) return;

  const hdr = $('hdr-state');
  hdr.textContent = d.running ? (d.healthy ? 'running' : 'degraded') : 'stopped';
  hdr.className = 'chip' + (d.running && d.healthy ? '' : ' urgent');
  $('hdr-height').textContent = d.chain_height ? `height ${d.chain_height}` : '';

  body.replaceChildren();
  body.append(el('div', 'big', d.registered ? 'Registered' : 'Not registered'));
  body.append(kvList([
    ['Address', d.guardian_address],
    ['Chain', d.chain_id],
    ['Version', d.version],
    ['Uptime', d.uptime],
    ['Accepting secrets', d.accepting_secrets ? 'yes' : 'no'],
    ['Chain height', d.chain_height],
    ['Processed height', d.last_block_height],
    // Lag is the number an operator acts on, so it is stated rather than
    // left to be worked out from the two heights above.
    ['Height lag', d.height_lag, d.height_lag > 2 ? 'stale' : null],
    ['Event stream', d.event_stream],
    ['Polling interval', d.polling_interval],
    ['Last update', when(d.last_update)],
  ]));
}

function renderEconomics(d) {
  const fb = $('float-body');
  const eb = $('exposure-body');
  if (handleUnavailable(fb, d)) {
    handleUnavailable(eb, d);
    return;
  }

  fb.replaceChildren();
  fb.append(el('div', 'big', veil(d.float_unlocked_uveil)));
  fb.append(el('p', 'faint', 'unlocked — what new bonds can draw on'));
  fb.append(kvList([
    ['Float total', veil(d.float_total_uveil)],
    ['Locked in bonds', veil(d.float_locked_uveil)],
    ['Bond multiplier k', d.bond_k_display + (d.bond_k_at_floor ? ' (at floor)' : '')],
    ['Reveals to floor', d.reveals_to_floor],
    ['Active bonds', `${d.active_bond_count} of ${d.bond_cap}`],
    ['Concurrency headroom', d.bond_headroom],
    ['Affordable bonds', d.affordable_bonds],
    ['Typical bond', d.typical_bond_uveil ? veil(d.typical_bond_uveil) : undefined],
    ['Signing balance', veil(d.signing_balance), d.signing_balance_low ? 'stale' : null],
  ]));
  if (d.signing_balance_low) {
    fb.append(el('p', 'warn',
      'Signing balance is low — confirmations and reveals are transactions, and an unfunded guardian misses windows.'));
  }

  eb.replaceChildren();
  eb.append(el('div', 'big', veil(d.total_bonded_uveil)));
  eb.append(el('p', 'faint', 'total bonded across active assignments'));
  eb.append(kvList([
    ['Largest single bond', veil(d.largest_bond_uveil)],
    ['Active bonds', d.active_bond_count],
    ['Bond multiplier k', d.bond_k_display],
  ]));
  eb.append(el('p', 'note',
    'A missed reveal is slashed as a percentage of the posted bond, and an early reveal forfeits it in full — so bonded total is the exposure that matters, not the float.'));
}

function assignmentRow(a) {
  return [
    a.secret_id,
    { node: chip(a.local_state, a.at_risk ? 'urgent' : 'calm') },
    a.chain_state,
    `${a.accepted_count} of ${a.min_shares}–${a.max_shares}`,
    blocks(a.blocks_to_commit_deadline),
    blocks(a.blocks_to_window_open),
    blocks(a.blocks_to_window_close),
    veil(a.bond_uveil),
    veil(a.reward_floor_uveil),
    a.revealed ? 'yes' : 'no',
  ];
}

const ASSIGNMENT_HEADERS = [
  'secret', 'local state', 'chain state', 'accepted', 'commit in',
  'opens in', 'closes in', 'bond', 'reward floor', 'revealed',
];

function renderAssignments(d) {
  const ab = $('assignments-body');
  const qb = $('queue-body');
  const rb = $('risk-body');
  const riskCard = $('c-risk');

  if (handleUnavailable(ab, d)) {
    handleUnavailable(qb, d);
    riskCard.hidden = true;
    return;
  }

  // 5. Active assignments
  ab.replaceChildren();
  if (!d.active || d.active.length === 0) {
    setEmpty(ab, 'No active assignments. Nothing is expected of this guardian right now.');
  } else {
    ab.append(table(ASSIGNMENT_HEADERS, d.active.map(assignmentRow)));
    ab.append(el('p', 'faint',
      'Reward floor is pool ÷ band ceiling — the least this pays if the roster fills. A smaller accepted roster divides the same pool fewer ways, so the actual share can only be higher.'));
  }

  // 2. Work queue
  qb.replaceChildren();
  const awaiting = d.awaiting_confirmation || [];
  const pending = d.pending_reveal || [];
  qb.append(kvList([
    ['Awaiting offline verification', awaiting.length],
    ['Accepted, reveal outstanding', pending.length],
    ['Current height', d.current_height],
  ]));
  if (pending.length) {
    qb.append(el('p', 'eyebrow', 'Reveal calendar'));
    qb.append(table(
      ['secret', 'opens at', 'planned reveal', 'closes at', 'opens in', 'closes in'],
      pending.map((a) => [
        a.secret_id,
        a.reveal_start_block,
        a.reveal_start_block,
        a.reveal_end_block,
        blocks(a.blocks_to_window_open),
        blocks(a.blocks_to_window_close),
      ]),
    ));
  }
  if (!awaiting.length && !pending.length) {
    qb.append(el('p', 'empty', 'Queue empty.'));
  }

  // 3. At-risk
  const risky = d.at_risk || [];
  riskCard.hidden = risky.length === 0;
  if (risky.length) {
    rb.replaceChildren();
    rb.append(table(
      ['secret', 'why', 'closes in', 'bond at stake'],
      risky.map((a) => [a.secret_id, a.risk_note, blocks(a.blocks_to_window_close), veil(a.bond_uveil)]),
    ));
  }
}

function renderKeys(d) {
  const kb = $('keys-body');
  const rot = $('rotation-body');
  if (handleUnavailable(kb, d)) {
    handleUnavailable(rot, d);
    return;
  }

  kb.replaceChildren();
  kb.append(el('div', 'big', d.fingerprints_match ? 'Key binding OK' : 'Key mismatch'));
  kb.append(kvList([
    ['Registered key', d.registered_fingerprint],
    ['Local key', d.local_fingerprint],
    ['Address', d.address],
  ]));
  if (!d.fingerprints_match) {
    kb.append(el('p', 'warn',
      'The local key does not match the key registered on chain. Shares encrypted to the registered key cannot be decrypted with this file — reveals will fail.'));
  }
  // Whether the key is encrypted at rest is deliberately not shown here: this
  // page has no credential in front of it, and "this guardian's key is in
  // plaintext" is a targeting signal. `guardianctl config doctor` answers it on
  // the host.

  rot.replaceChildren();
  rot.append(el('div', 'big', `Epoch ${d.current_epoch}`));
  rot.append(kvList([
    ['Rotation eligible', d.rotation_eligible ? 'yes' : 'no'],
    ['Assignments on older epochs', d.outgoing_epoch_assignments],
  ]));
  if (d.rotation_note) rot.append(el('p', 'note', d.rotation_note));
  if (d.epochs && d.epochs.length) {
    rot.append(table(
      ['epoch', 'effective from', 'fingerprint'],
      d.epochs.map((e) => [
        e.epoch + (e.current ? ' (current)' : ''),
        e.effective_from_height,
        e.fingerprint,
      ]),
    ));
  }
  rot.append(el('p', 'faint', 'Rotating is a CLI action: guardianctl rotate-key.'));
}

function renderRegistration(d) {
  const body = $('registration-body');
  if (handleUnavailable(body, d)) return;

  body.replaceChildren();
  body.append(kvList([
    ['Available from', d.available_from],
    ['Available until', d.available_until],
    ['Blocks remaining', d.blocks_remaining],
    ['Fields drifting from chain', d.drift_count, d.drift_count ? 'drift' : 'ok'],
    ['Config validates', d.config_valid ? 'yes' : 'no'],
  ]));
  if (d.eligibility_warning) {
    body.append(el('p', 'warn', d.eligibility_note ||
      'Availability is short enough to be excluding this guardian from long-dated secrets already.'));
  }
  if (!d.config_valid) {
    // The complaint itself stays on the host: validation messages quote the
    // value that failed, which is how a path or an endpoint would reach a page
    // that carries neither.
    body.append(el('p', 'warn',
      'The local configuration does not validate. Run guardianctl config doctor --config-only on the host for the reason.'));
  }
  const fields = d.fields || [];
  if (fields.length) {
    body.append(table(
      ['setting', 'local', 'on chain', ''],
      fields.map((f) => [
        f.name,
        f.local,
        f.chain === undefined || f.chain === '' ? '—' : f.chain,
        { node: f.drift ? chip('drift', 'urgent') : el('span', 'faint', f.note || '') },
      ]),
    ));
  }
  body.append(el('p', 'faint', 'Display only — renewal and edits are CLI actions.'));
}

function renderActivity(d) {
  const sub = $('submissions-body');
  const dec = $('decisions-body');
  const set = $('settlements-body');
  if (handleUnavailable(sub, d)) {
    handleUnavailable(dec, d);
    handleUnavailable(set, d);
    return;
  }

  const shownOf = (shown, total) =>
    total > shown ? `Showing the most recent ${shown} of ${total} since start.` : d.note;

  sub.replaceChildren();
  const subs = d.submissions || [];
  if (!subs.length) {
    setEmpty(sub, 'No transactions submitted since this process started.');
  } else {
    sub.append(table(
      ['when', 'kind', 'secret', 'result', 'tx', 'height'],
      subs.map((s) => [
        when(s.at), s.kind, s.secret_id,
        { node: chip(s.success ? 'ok' : 'failed', s.success ? 'calm' : 'urgent') },
        s.tx_hash || '—', s.height || '—',
      ]),
    ));
    sub.append(el('p', 'faint', shownOf(subs.length, d.total_submissions)));
  }

  dec.replaceChildren();
  const decs = d.decisions || [];
  if (!decs.length) {
    setEmpty(dec, 'No accept/reject decisions since this process started.');
  } else {
    dec.append(table(
      ['when', 'secret', 'outcome', 'reason', 'height'],
      decs.map((x) => [
        when(x.at), x.secret_id,
        { node: chip(x.outcome, x.outcome === 'accepted' ? 'calm' : 'urgent') },
        x.reason || '—', x.height || '—',
      ]),
    ));
    dec.append(el('p', 'faint', shownOf(decs.length, d.total_decisions)));
  }

  set.replaceChildren();
  const sets = d.settlements || [];
  if (!sets.length) {
    setEmpty(set, 'No settlements observed since this process started.');
  } else {
    set.append(table(
      ['when', 'secret', 'outcome', 'height'],
      sets.map((x) => [
        when(x.at), x.secret_id,
        { node: x.stalled ? chip('stalled', 'urgent') : el('span', null, x.outcome) },
        x.height || '—',
      ]),
    ));
    set.append(el('p', 'faint', shownOf(sets.length, d.total_settlements)));
  }
}

/* ---------- polling ---------- */

const SECTIONS = [
  ['vitals', renderVitals],
  ['assignments', renderAssignments],
  ['economics', renderEconomics],
  ['keys', renderKeys],
  ['registration', renderRegistration],
  ['activity', renderActivity],
];

async function pollOnce() {
  await Promise.all(SECTIONS.map(async ([name, render]) => {
    try {
      const res = await fetch(`api/${name}`, { cache: 'no-store' });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      render(await res.json());
    } catch (err) {
      // A section that cannot be fetched is reported as unavailable through
      // the same path the daemon uses, so the failure looks the same to the
      // operator whether the daemon or the browser lost the connection.
      render({ unavailable: true, reason: String(err && err.message ? err.message : err) });
    }
  }));
}

function start() {
  void pollOnce();
  setInterval(() => void pollOnce(), POLL_MS);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', start);
} else {
  start();
}
