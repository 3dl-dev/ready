// terminal.js — Accordion terminal playback from real rd demo transcripts
(function() {
  'use strict';

  var DEMOS = {
    solo: {
      title: 'Solo workflow',
      subtitle: 'nostr-native',
      source: 'https://github.com/3dl-dev/ready/blob/main/scripts/nostr-rd-roundtrip-demo.sh',
      lines: [
        { type: 'comment', text: '# Initialize a project — no server, no key exchange' },
        { type: 'cmd', text: 'rd init --name myproject' },
        { type: 'output', text: 'initialized myproject (nostr-native)' },
        { type: 'output', text: '  board: 30301:a9f766ae56bb...:myproject' },
        { type: 'output', text: '  owner: a9f766ae56bb...' },
        { type: 'output', text: '  log:   .ready/nostr-log.jsonl' },
        { type: 'blank' },
        { type: 'comment', text: '# Create a work item — signed event appended to the log' },
        { type: 'cmd', text: 'rd create "Ship login page" --priority p1 --type task' },
        { type: 'output', text: 'created myproject-6b7' },
        { type: 'blank' },
        { type: 'comment', text: '# What needs attention?' },
        { type: 'cmd', text: 'rd ready' },
        { type: 'output', text: '  myproject-6b7     p1        inbox                   Ship login page' },
        { type: 'blank' },
        { type: 'comment', text: '# Claim it' },
        { type: 'cmd', text: 'rd claim myproject-6b7' },
        { type: 'output', text: 'claimed myproject-6b7' },
        { type: 'blank' },
        { type: 'comment', text: '# Done — close with a reason' },
        { type: 'cmd', text: 'rd done myproject-6b7 --reason "Login page ships with JWT auth"' },
        { type: 'output', text: 'closed myproject-6b7 (done)' },
      ]
    },
    team: {
      title: 'Team with invite tokens',
      subtitle: 'self-mint invite',
      source: 'https://github.com/3dl-dev/ready/blob/main/scripts/nostr-grant-revoke-demo.sh',
      lines: [
        { type: 'comment', text: '# Owner mints a one-use claim token — ships NO secret key' },
        { type: 'cmd', text: 'rd invite' },
        { type: 'output', text: 'rd1_eyJ2IjozLCJib2FyZCI6IjMwMzAxOmE5Zjc2NmFlNTZiYmY0Ni...' },
        { type: 'output', text: '' },
        { type: 'output', text: 'Share this token with the joiner. They run `rd join <token>`' },
        { type: 'output', text: '(self-mints, read-only) and send back a pubkey + claim-nonce.' },
        { type: 'blank' },
        { type: 'comment', text: '# Teammate joins — self-mints its OWN key, reads the board read-only' },
        { type: 'cmd', text: 'rd join rd1_eyJ2IjozLCJib2FyZCI6...' },
        { type: 'output', text: 'Joined board a9f766ae56bb... READ-ONLY (invite expires in 1h59m0s).' },
        { type: 'output', text: '  run \'rd ready\' to see the project\'s items now.' },
        { type: 'output', text: '' },
        { type: 'output', text: 'To get WRITE access, send the owner this:' },
        { type: 'output', text: '  pubkey=73fd6d99fa595c00dd414072799762add77d6c3e07856ac0f0455c5c7e3b2ef6' },
        { type: 'output', text: '  claim=a04cf1027ba1aa31ca3d8558486e90fb' },
        { type: 'blank' },
        { type: 'comment', text: '# Owner grants write access — binds the nonce to that ONE key' },
        { type: 'cmd', text: 'rd grant 73fd6d99fa59... contributor --claim a04cf1027ba1...' },
        { type: 'output', text: 'published role-grant: grantee=73fd6d99fa59... role=contributor' },
        { type: 'output', text: '  event id=8d97392866... relay-accepted=true' },
        { type: 'output', text: 'regenerated relay write-allowlist (2 key(s) admitted)' },
        { type: 'blank' },
        { type: 'comment', text: '# Teammate now has write access — claims the item' },
        { type: 'cmd', text: 'rd ready' },
        { type: 'output', text: '  backend-776      p1        inbox                   Build API' },
        { type: 'cmd', text: 'rd claim backend-776' },
        { type: 'output', text: 'claimed backend-776' },
      ]
    },
    gate: {
      title: 'Agent escalation',
      subtitle: 'human gates',
      source: 'https://github.com/3dl-dev/ready/blob/main/scripts/nostr-trust-gate-demo.sh',
      lines: [
        { type: 'comment', text: '# Agent hits a decision point — gate types:' },
        { type: 'comment', text: '#   budget design scope review human stall periodic' },
        { type: 'cmd', text: 'rd gate myapp-dd6 --gate-type design \\' },
        { type: 'cmd-cont', text: '  --description "Option A saves 2ms but breaks caching."' },
        { type: 'output', text: 'gate sent for myapp-dd6 (design)' },
        { type: 'blank' },
        { type: 'comment', text: '# Item moves to waiting' },
        { type: 'cmd', text: 'rd show myapp-dd6' },
        { type: 'output', text: 'Status:   waiting' },
        { type: 'output', text: 'Waiting on: Option A saves 2ms but breaks caching. (gate)' },
        { type: 'blank' },
        { type: 'comment', text: '# Human sees it from anywhere' },
        { type: 'cmd', text: 'rd gates' },
        { type: 'output', text: '  myapp-dd6         p1        Option A saves 2ms but breaks cac...  Migrate auth' },
        { type: 'blank' },
        { type: 'comment', text: '# Human approves — agent continues' },
        { type: 'cmd', text: 'rd approve myapp-dd6 --reason "Use option B. Safety over 2ms."' },
        { type: 'output', text: 'approved gate for myapp-dd6' },
        { type: 'blank' },
        { type: 'comment', text: '# Agent: item is active again' },
        { type: 'cmd', text: 'rd show myapp-dd6' },
        { type: 'output', text: 'Status:   active' },
      ]
    },
    isolation: {
      title: 'Self-mint & owner grant',
      subtitle: '$RD_HOME / $RD_ACTOR',
      source: 'https://github.com/3dl-dev/ready/blob/main/scripts/nostr-grant-revoke-demo.sh',
      lines: [
        { type: 'comment', text: '# Identity is a secp256k1 key in $RD_HOME (default ~/.config/rd).' },
        { type: 'comment', text: '# $RD_ACTOR selects which key signs. A self-minted key is INERT' },
        { type: 'comment', text: '# until the owner grants it \u2014 admission is the trust boundary.' },
        { type: 'blank' },
        { type: 'comment', text: '# Owner creates an item \u2014 signs with the board-author (owner) key' },
        { type: 'cmd', text: 'rd create "Fix auth bug" --priority p1 --type task' },
        { type: 'output', text: 'myproject-638' },
        { type: 'blank' },
        { type: 'comment', text: '# An agent walks up. $RD_ACTOR self-mints keys/agent-pm.json and claims' },
        { type: 'cmd', text: 'RD_ACTOR=agent:pm rd claim myproject-638' },
        { type: 'output', text: 'claimed myproject-638' },
        { type: 'blank' },
        { type: 'comment', text: '# ...but the ungranted key is INERT \u2014 the claim is dropped on read-back:' },
        { type: 'comment', text: '# Status stays inbox, no By: attribution.' },
        { type: 'cmd', text: 'rd show myproject-638' },
        { type: 'output', text: 'Status:   inbox' },
        { type: 'output', text: 'For:      a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25' },
        { type: 'blank' },
        { type: 'comment', text: '# Owner admits that ONE key (pubkey from $RD_HOME/keys/agent-pm.json)' },
        { type: 'cmd', text: 'rd grant 3933c96d7fe0... contributor --label agent:pm' },
        { type: 'output', text: 'published role-grant: grantee=3933c96d7fe0... role=contributor' },
        { type: 'output', text: '  event id=e4c07e2326a9... relay-accepted=true' },
        { type: 'output', text: 'regenerated relay write-allowlist (2 key(s) admitted)' },
        { type: 'blank' },
        { type: 'comment', text: '# The grant retroactively honors the claim \u2014 now active, attributed' },
        { type: 'comment', text: '# to the agent\u2019s DISTINCT secp256k1 key (owner is still For:).' },
        { type: 'cmd', text: 'rd show myproject-638' },
        { type: 'output', text: 'Status:   active' },
        { type: 'output', text: 'For:      a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25' },
        { type: 'output', text: 'By:       3933c96d7fe04edf3c93a3150cd6eeee8fa073c00a9c4fee35ca0355c3a82219' },
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
