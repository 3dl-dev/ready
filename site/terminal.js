// terminal.js — Accordion terminal playback from real rd demo transcripts
(function() {
  'use strict';

  var DEMOS = {
    solo: {
      title: 'Solo workflow',
      subtitle: '01-solo.sh',
      source: 'https://github.com/3dl-dev/ready/blob/main/test/demo/01-solo.sh',
      lines: [
        { type: 'comment', text: '# Initialize a project' },
        { type: 'cmd', text: 'rd init --name myproject' },
        { type: 'output', text: 'initialized myproject' },
        { type: 'output', text: '  campfire: 7b0929f77f95...' },
        { type: 'output', text: '  declarations: 16 operations published' },
        { type: 'blank' },
        { type: 'comment', text: '# Create a work item — bare ID returned' },
        { type: 'cmd', text: 'ITEM=$(rd create "Ship login page" --priority p1 --type task)' },
        { type: 'cmd', text: 'echo $ITEM' },
        { type: 'output', text: 'myproject-e1f' },
        { type: 'blank' },
        { type: 'comment', text: '# What needs attention?' },
        { type: 'cmd', text: 'rd ready' },
        { type: 'output', text: '  myproject-e1f  p1  inbox  3h  Ship login page' },
        { type: 'blank' },
        { type: 'comment', text: '# Claim it' },
        { type: 'cmd', text: 'rd claim $ITEM' },
        { type: 'output', text: 'claimed myproject-e1f' },
        { type: 'blank' },
        { type: 'comment', text: '# Done — close with a reason' },
        { type: 'cmd', text: 'rd done $ITEM --reason "Login page ships with JWT auth"' },
        { type: 'output', text: 'closed myproject-e1f (done)' },
      ]
    },
    team: {
      title: 'Team with invite tokens',
      subtitle: '02-team.sh',
      source: 'https://github.com/3dl-dev/ready/blob/main/test/demo/02-team.sh',
      lines: [
        { type: 'comment', text: '# Owner creates project and generates invite' },
        { type: 'cmd', text: 'rd init --name backend' },
        { type: 'output', text: 'initialized backend' },
        { type: 'cmd', text: 'TOKEN=$(rd invite)' },
        { type: 'blank' },
        { type: 'comment', text: '# Teammate joins — one command, no key exchange' },
        { type: 'cmd', text: 'rd join $TOKEN' },
        { type: 'output', text: 'joined 00d5716f0154... via invite token (expires in 1h59m)' },
        { type: 'blank' },
        { type: 'comment', text: '# Owner creates work and delegates' },
        { type: 'cmd', text: 'ITEM=$(rd create "Build API" --type task --priority p1)' },
        { type: 'cmd', text: 'rd delegate $ITEM --to <member-identity>' },
        { type: 'output', text: 'delegated backend-776 to 6d2d5a5f4c38...' },
        { type: 'blank' },
        { type: 'comment', text: '# Member sees it — auto-synced, no manual pull' },
        { type: 'cmd', text: 'cd member/ && rd ready' },
        { type: 'output', text: '  backend-776  p1  inbox  3h  Build API' },
        { type: 'blank' },
        { type: 'comment', text: '# Member claims and completes' },
        { type: 'cmd', text: 'rd claim backend-776' },
        { type: 'output', text: 'claimed backend-776' },
        { type: 'cmd', text: 'rd done backend-776 --reason "API complete"' },
        { type: 'output', text: 'closed backend-776 (done)' },
      ]
    },
    gate: {
      title: 'Agent escalation',
      subtitle: '06-gate-escalation.sh',
      source: 'https://github.com/3dl-dev/ready/blob/main/test/demo/06-gate-escalation.sh',
      lines: [
        { type: 'comment', text: '# Agent hits a decision point' },
        { type: 'cmd', text: 'rd gate myapp-dd6 --gate-type design \\' },
        { type: 'cmd-cont', text: '  --description "Option A saves 2ms but breaks caching."' },
        { type: 'output', text: '{"gate_type":"design","id":"myapp-dd6","status":"waiting"}' },
        { type: 'blank' },
        { type: 'comment', text: '# Item moves to waiting' },
        { type: 'cmd', text: 'rd show myapp-dd6' },
        { type: 'output', text: 'Status:   waiting' },
        { type: 'output', text: 'Waiting:  Option A saves 2ms but breaks caching... (gate)' },
        { type: 'blank' },
        { type: 'comment', text: '# Human sees it from anywhere' },
        { type: 'cmd', text: 'rd gates' },
        { type: 'output', text: '  myapp-dd6  p1  design  Option A saves 2ms...  Migrate auth' },
        { type: 'blank' },
        { type: 'comment', text: '# Human approves — agent continues' },
        { type: 'cmd', text: 'rd approve myapp-dd6 --reason "Use option B. Safety over 2ms."' },
        { type: 'output', text: '{"id":"myapp-dd6","resolution":"approved"}' },
        { type: 'blank' },
        { type: 'comment', text: '# Agent: item is active again' },
        { type: 'cmd', text: 'rd show myapp-dd6' },
        { type: 'output', text: 'Status:   active' },
      ]
    },
    isolation: {
      title: 'Walk-up agent isolation',
      subtitle: '11-filesystem-isolation.sh',
      source: 'https://github.com/3dl-dev/ready/blob/main/test/demo/11-filesystem-isolation.sh',
      lines: [
        { type: 'comment', text: '# Project directory with two agent worktrees' },
        { type: 'output', text: 'myproject/' },
        { type: 'output', text: '  .campfire/root              \u2190 project (shared)' },
        { type: 'output', text: '  .cf/identity.json           \u2190 owner' },
        { type: 'output', text: '  worktree-a/.cf/identity.json  \u2190 agent A' },
        { type: 'output', text: '  worktree-b/.cf/identity.json  \u2190 agent B' },
        { type: 'blank' },
        { type: 'comment', text: '# Agent A: cd and run. Walk-up handles everything.' },
        { type: 'cmd', text: 'cd myproject/worktree-a' },
        { type: 'cmd', text: 'rd ready' },
        { type: 'output', text: '  myproject-460  p1  inbox  3h  Implement login' },
        { type: 'output', text: '  myproject-b22  p1  inbox  3h  Write tests' },
        { type: 'blank' },
        { type: 'comment', text: '# Agent A claims one item' },
        { type: 'cmd', text: 'ITEM=$(rd ready | head -1)' },
        { type: 'cmd', text: 'rd claim $ITEM' },
        { type: 'output', text: 'claimed myproject-460' },
        { type: 'blank' },
        { type: 'comment', text: '# Agent B: same project, different identity, zero config' },
        { type: 'cmd', text: 'cd myproject/worktree-b' },
        { type: 'cmd', text: 'rd claim myproject-b22' },
        { type: 'output', text: 'claimed myproject-b22' },
        { type: 'blank' },
        { type: 'comment', text: '# Owner sees both — different identities, same queue' },
        { type: 'cmd', text: 'rd list --all --json | jq ".[].by"' },
        { type: 'output', text: '"a3f2...agent-a"' },
        { type: 'output', text: '"7c1e...agent-b"' },
      ]
    }
  };

  var CHAR_DELAY = 20;
  var LINE_PAUSE = 100;
  var CMD_PAUSE = 350;
  var SECTION_PAUSE = 500;
  var RESTART_DELAY = 4000;

  function TerminalPlayer(el, demo) {
    this.el = el;
    this.demo = demo;
    this.lines = demo.lines;
    this.playing = false;
    this.paused = false;
    this.step = 0;
    this.abortFn = null;
    this.build();
  }

  TerminalPlayer.prototype.build = function() {
    this.el.innerHTML = '';

    // Accordion header
    var header = document.createElement('div');
    header.className = 'term-accordion-header';
    var self = this;
    header.onclick = function() { toggleAccordion(self.el); };

    var arrow = document.createElement('span');
    arrow.className = 'term-accordion-arrow';
    arrow.textContent = '\u25B6';

    var title = document.createElement('span');
    title.className = 'term-accordion-title';
    title.textContent = this.demo.title;

    var subtitle = document.createElement('span');
    subtitle.className = 'term-accordion-subtitle';
    subtitle.textContent = this.demo.subtitle;

    header.appendChild(arrow);
    header.appendChild(title);
    header.appendChild(subtitle);

    // Accordion body (viewport + controls)
    this.body = document.createElement('div');
    this.body.className = 'term-accordion-body';

    // Viewport
    this.viewport = document.createElement('div');
    this.viewport.className = 'term-viewport';

    this.content = document.createElement('div');
    this.content.className = 'term-content';

    this.cursorLine = document.createElement('div');
    this.cursorLine.className = 'term-line';
    this.cursorLine.innerHTML = '<span class="t-ps1">$ </span><span class="t-cursor"></span>';
    this.content.appendChild(this.cursorLine);

    this.spacer = document.createElement('div');
    this.spacer.className = 'term-spacer';
    this.viewport.appendChild(this.spacer);
    this.viewport.appendChild(this.content);

    // Controls
    var controls = document.createElement('div');
    controls.className = 'term-bar';

    this.playBtn = document.createElement('button');
    this.playBtn.className = 'term-btn';
    this.playBtn.innerHTML = '\u25B6';
    this.playBtn.title = 'Play';
    this.playBtn.onclick = function(e) { e.stopPropagation(); self.togglePlay(); };

    this.restartBtn = document.createElement('button');
    this.restartBtn.className = 'term-btn';
    this.restartBtn.innerHTML = '\u23EE';
    this.restartBtn.title = 'Restart';
    this.restartBtn.onclick = function(e) { e.stopPropagation(); self.restart(); };

    this.progressWrap = document.createElement('div');
    this.progressWrap.className = 'term-progress-wrap';
    this.progressFill = document.createElement('div');
    this.progressFill.className = 'term-progress-fill';
    this.progressWrap.appendChild(this.progressFill);
    this.progressWrap.onclick = function(e) {
      e.stopPropagation();
      var rect = self.progressWrap.getBoundingClientRect();
      self.scrubTo((e.clientX - rect.left) / rect.width);
    };

    this.stepLabel = document.createElement('span');
    this.stepLabel.className = 'term-step';
    this.stepLabel.textContent = '0/' + this.lines.length;

    var sourceLink = document.createElement('a');
    sourceLink.href = this.demo.source;
    sourceLink.className = 'term-bar-source';
    sourceLink.textContent = 'source';
    sourceLink.target = '_blank';
    sourceLink.rel = 'noopener';
    sourceLink.onclick = function(e) { e.stopPropagation(); };

    controls.appendChild(this.playBtn);
    controls.appendChild(this.restartBtn);
    controls.appendChild(this.progressWrap);
    controls.appendChild(this.stepLabel);
    controls.appendChild(sourceLink);

    this.body.appendChild(this.viewport);
    this.body.appendChild(controls);

    this.el.appendChild(header);
    this.el.appendChild(this.body);
  };

  TerminalPlayer.prototype.updateProgress = function() {
    var pct = this.lines.length > 0 ? (this.step / this.lines.length) * 100 : 0;
    this.progressFill.style.width = pct + '%';
    this.stepLabel.textContent = this.step + '/' + this.lines.length;
  };

  TerminalPlayer.prototype.togglePlay = function() {
    if (this.playing && !this.paused) { this.pause(); }
    else if (this.paused) { this.resume(); }
    else { this.play(); }
  };

  TerminalPlayer.prototype.play = function() {
    this.playing = true;
    this.paused = false;
    this.playBtn.innerHTML = '\u275A\u275A';
    this.step = 0;
    this.content.innerHTML = '';
    this.content.appendChild(this.cursorLine);
    this.runNext();
  };

  TerminalPlayer.prototype.pause = function() {
    this.paused = true;
    this.playBtn.innerHTML = '\u25B6';
    if (this.abortFn) this.abortFn();
  };

  TerminalPlayer.prototype.resume = function() {
    this.paused = false;
    this.playBtn.innerHTML = '\u275A\u275A';
    this.runNext();
  };

  TerminalPlayer.prototype.stop = function() {
    this.playing = false;
    this.paused = false;
    this.playBtn.innerHTML = '\u25B6';
    if (this.abortFn) this.abortFn();
  };

  TerminalPlayer.prototype.restart = function() {
    this.stop();
    this.play();
  };

  TerminalPlayer.prototype.scrubTo = function(pct) {
    this.stop();
    var target = Math.max(0, Math.min(Math.floor(pct * this.lines.length), this.lines.length));
    this.content.innerHTML = '';
    this.step = 0;
    for (var i = 0; i < target; i++) {
      this.renderLineInstant(this.lines[i]);
      this.step = i + 1;
    }
    this.content.appendChild(this.cursorLine);
    this.updateProgress();
    this.scrollToBottom();
  };

  TerminalPlayer.prototype.renderLineInstant = function(line) {
    var el = document.createElement('div');
    el.className = 'term-line';
    if (line.type === 'blank') { el.className += ' term-blank'; el.innerHTML = '\u00A0'; }
    else if (line.type === 'comment') { el.className += ' t-comment'; el.textContent = line.text; }
    else if (line.type === 'cmd') { el.className += ' t-cmd'; el.innerHTML = '<span class="t-ps1">$ </span>' + this.esc(line.text); }
    else if (line.type === 'cmd-cont') { el.className += ' t-cmd'; el.innerHTML = '<span class="t-ps1">  </span>' + this.esc(line.text); }
    else if (line.type === 'output') { el.className += ' t-out'; el.textContent = line.text; }
    // Insert before cursor if it's in the DOM, otherwise just append
    if (this.cursorLine.parentNode === this.content) {
      this.content.insertBefore(el, this.cursorLine);
    } else {
      this.content.appendChild(el);
    }
  };

  TerminalPlayer.prototype.runNext = function() {
    if (!this.playing || this.paused) return;
    if (this.step >= this.lines.length) {
      this.updateProgress();
      var self = this;
      this.delay(RESTART_DELAY, function() { if (self.playing && !self.paused) self.play(); });
      return;
    }
    var line = this.lines[this.step];
    var self = this;
    if (line.type === 'blank') {
      this.renderLineInstant(line);
      this.step++; this.updateProgress();
      this.delay(SECTION_PAUSE, function() { self.runNext(); });
    } else if (line.type === 'comment' || line.type === 'output') {
      this.renderLineInstant(line);
      this.scrollToBottom();
      this.step++; this.updateProgress();
      this.delay(LINE_PAUSE, function() { self.runNext(); });
    } else if (line.type === 'cmd' || line.type === 'cmd-cont') {
      this.typeCmd(line, function() {
        self.step++; self.updateProgress();
        self.delay(CMD_PAUSE, function() { self.runNext(); });
      });
    }
  };

  TerminalPlayer.prototype.typeCmd = function(line, cb) {
    var isCont = line.type === 'cmd-cont';
    var el = document.createElement('div');
    el.className = 'term-line t-cmd';
    var ps1 = document.createElement('span');
    ps1.className = 't-ps1';
    ps1.textContent = isCont ? '  ' : '$ ';
    el.appendChild(ps1);
    var textSpan = document.createElement('span');
    el.appendChild(textSpan);
    var cursor = document.createElement('span');
    cursor.className = 't-cursor';
    el.appendChild(cursor);

    this.content.removeChild(this.cursorLine);
    this.content.appendChild(el);
    this.scrollToBottom();

    var text = line.text, pos = 0, self = this;
    function next() {
      if (!self.playing || self.paused) return;
      if (pos >= text.length) {
        cursor.remove();
        self.content.appendChild(self.cursorLine);
        self.scrollToBottom();
        cb();
        return;
      }
      textSpan.textContent += text[pos++];
      self.scrollToBottom();
      self.delay(CHAR_DELAY, next);
    }
    next();
  };

  TerminalPlayer.prototype.delay = function(ms, fn) {
    var id = setTimeout(fn, ms);
    this.abortFn = function() { clearTimeout(id); };
  };

  TerminalPlayer.prototype.scrollToBottom = function() {
    this.viewport.scrollTop = this.viewport.scrollHeight;
  };

  TerminalPlayer.prototype.esc = function(s) {
    var d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
  };

  // Accordion: only one open at a time
  var allPlayers = [];

  function toggleAccordion(el) {
    var wasActive = el.classList.contains('active');

    // Close all
    var all = document.querySelectorAll('[data-terminal-demo]');
    for (var i = 0; i < all.length; i++) {
      all[i].classList.remove('active');
      if (all[i]._player) all[i]._player.stop();
    }

    // Open clicked (if it wasn't already open)
    if (!wasActive) {
      el.classList.add('active');
    }
  }

  // Init
  document.addEventListener('DOMContentLoaded', function() {
    var els = document.querySelectorAll('[data-terminal-demo]');
    for (var i = 0; i < els.length; i++) {
      var name = els[i].getAttribute('data-terminal-demo');
      if (DEMOS[name]) {
        var player = new TerminalPlayer(els[i], DEMOS[name]);
        els[i]._player = player;
        allPlayers.push(player);
      }
    }
  });
})();
