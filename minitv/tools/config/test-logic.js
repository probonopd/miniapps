/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: BSD-2-Clause
 */
// Unit tests for the pure parts of minitv-config.html (schedule logic and MQTT codec): node test-logic.js
'use strict';
// Times below are checked in the time zone of the TV's family.
process.env.TZ = 'Europe/Berlin';
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');

const html = fs.readFileSync(require('path').join(__dirname, 'minitv-config.html'), 'utf8');
function block(name) {
  const m = new RegExp(`/\\* ==== BEGIN ${name}[^]*?\\*/([^]*?)/\\* ==== END ${name} ==== \\*/`).exec(html);
  if (!m) throw new Error('block not found: ' + name);
  return m[1];
}
const ctx = { TextEncoder, TextDecoder, Promise };
vm.createContext(ctx);
vm.runInContext(block('LOGIC') + block('MQTT') + block('SYNC') + ';this.Logic = Logic; this.Mqtt = Mqtt; this.Sync = Sync;', ctx);
const { Logic, Mqtt, Sync } = ctx;
const H = Logic.parseHM, F = Logic.fmtHM;
let n = 0;
function eq(a, b, msg) { assert.deepStrictEqual(JSON.parse(JSON.stringify(a)), b, msg); n++; }

// Times
eq(H('8:05'), 485); eq(H('23:59'), 1439); eq(H('24:00'), -1); eq(H(''), -1); eq(H(null), -1); eq(F(1440), '00:00');

// Daily: from == HH:mm, to = +1 minute, every 1
const daily = { from: '12:00', to: '12:01', every: 1 };
eq(Logic.fireTimes(daily).map(F), ['12:00']);
eq(Logic.summary(daily), 'Täglich um 12:00');
eq(Logic.preview(daily, H('11:00')), 'Heute noch: 12:00');
eq(Logic.preview(daily, H('12:00')), 'Heute nicht mehr. Wieder morgen um 12:00.');
eq(Logic.whenFromStored(daily).kind, 'daily');
eq(Logic.whenToStored(Logic.whenFromStored(daily)), daily);
// Daily at 23:59 crosses into 00:00 but still fires once
eq(Logic.fireTimes({ from: '23:59', to: '00:00', every: 1 }).map(F), ['23:59']);

// Hourly 08:00 to 20:00 inclusive in the editor -> to 20:01
const hourly = Logic.whenToStored({ kind: 'repeat', from: H('08:00'), until: H('20:00'), allDay: false, n: 1, unit: 'h' });
eq(hourly, { from: '08:00', to: '20:01', every: 60 });
eq(Logic.fireTimes(hourly).length, 13);
eq(Logic.summary(hourly), 'Jede Stunde von 08:00 bis 20:00 (um 08:00, 09:00, 10:00, 11:00, ... 20:00; 13\u00a0Mal am Tag)');
eq(Logic.preview(hourly, H('17:30')), 'Heute noch: 18:00, 19:00, 20:00');
eq(Logic.preview(hourly, H('06:00')), 'Heute noch: 08:00, 09:00, 10:00, 11:00, 12:00, 13:00, 14:00, 15:00 und 5 weitere');
// The app's own "to" is exclusive: 08:00-20:00 every 60 ends at 19:00
eq(Logic.fireTimes({ from: '08:00', to: '20:00', every: 60 }).map(F).pop(), '19:00');

// Midnight crossing: 21:00 to 05:00 every 30 (README example)
const night = { from: '21:00', to: '05:00', every: 30 };
const t = Logic.fireTimes(night).map(F);
eq([t[0], t[5], t[t.length - 1], t.length], ['21:00', '23:30', '04:30', 16]);
eq(Logic.crossesMidnight(Logic.whenFromStored(night)), true);
eq(Logic.nextTimesToday(night, H('02:10')).map(F).slice(0, 3), ['02:30', '03:00', '03:30']);
eq(Logic.preview(night, H('22:50')), 'Heute noch: 23:00, 23:30');
eq(Logic.preview(night, H('23:45')), 'Heute nicht mehr. Wieder morgen um 00:00.');
eq(Logic.summary(night), 'Alle 30 Minuten von 21:00 bis 04:59 (um 21:00, 21:30, 22:00, 22:30, ... 04:30; 16\u00a0Mal am Tag)');
eq(Logic.crossesMidnight(Logic.whenFromStored(hourly)), false);

// from == to: all day
const allDay = { from: '06:00', to: '06:00', every: 120 };
eq(Logic.fireTimes(allDay).map(F), ['06:00', '08:00', '10:00', '12:00', '14:00', '16:00', '18:00', '20:00', '22:00', '00:00', '02:00', '04:00']);
const wa = Logic.whenFromStored(allDay);
eq([wa.kind, wa.allDay, wa.n, wa.unit], ['repeat', true, 2, 'h']);
eq(Logic.whenToStored(wa), allDay);
eq(Logic.summary(allDay).startsWith('Alle 2 Stunden, den ganzen Tag ab 06:00'), true);
eq(Logic.fireTimes({ from: '00:00', to: '00:00', every: 1 }).length, 1440);

// Invalid
eq(Logic.fireTimes({ from: 'x', to: '05:00', every: 30 }), []);
eq(Logic.fireTimes({ from: '01:00', to: '05:00', every: 0 }), []);
eq(Logic.summary({ from: '', to: '', every: 1 }), 'Die Zeit ist noch nicht vollständig.');

// Ids
eq(Logic.slugify('Zeit zum Mittagessen. Guten Appetit!'), 'zeit-zum-mittagessen');
eq(Logic.slugify('Tür schließen, ß'), 'tuer-schliessen-ss');
eq(Logic.slugify('!!!'), 'erinnerung');
const ms = [{ id: 'a', text: 'x' }, { id: 'a', text: 'Trinken' }, { id: null, idBase: 'trinken', text: 'y' }];
Logic.ensureIds(ms);
eq(ms.map(m => m.id), ['a', 'trinken', 'trinken-2']);

// Defaults are left out of the JSON
const d = Logic.presDefaults({ message_mode: 'text', message_seconds: 0, speech_repeat: 1, speech_pause: 5 });
const raw = { id: 'bettzeit', text: 'Bettzeit', from: '21:00', to: '05:00', every: 30, enabled: true, mode: 'both', show_seconds: 0, repeat: 2, pause: 5, extra_key: 7 };
const m = Logic.modelFromStored(raw, d);
eq(Logic.storedFromModel(m, d), { id: 'bettzeit', text: 'Bettzeit', from: '21:00', to: '05:00', every: 30, enabled: true, mode: 'both', repeat: 2, extra_key: 7 });
eq(Logic.presFromMessage({ speak: true }, d).mode, 'both');
eq(Logic.presFromMessage({ speak: true, mode: 'text' }, d).mode, 'text');
eq(Logic.messageFromPres(' Hallo ', { mode: 'speech', keyOnly: false, seconds: 9, repeat: 1, pause: 5 }, d), { text: 'Hallo', mode: 'speech' });
eq(Logic.messageFromPres('Hi', { mode: 'text', keyOnly: false, seconds: 9, repeat: 3, pause: 5 }, d), { text: 'Hi', show_seconds: 9 });

// Presets
const fenster = Logic.modelsFromPreset({ id: 'fenster', text: 'Lüften', at: ['09:00', '17:00'], mode: 'text' }, d);
Logic.ensureIds(fenster);
eq(fenster.map(x => Logic.storedFromModel(x, d)), [
  { id: 'fenster', text: 'Lüften', from: '09:00', to: '09:01', every: 1, enabled: true },
  { id: 'fenster-2', text: 'Lüften', from: '17:00', to: '17:01', every: 1, enabled: true }]);
const bett = Logic.modelsFromPreset({ id: 'bettzeit', text: 'Es ist Bettzeit.', from: '21:00', until: '05:00', every: 30, mode: 'both' }, d)[0];
eq(Logic.storedFromModel(bett, d), { text: 'Es ist Bettzeit.', from: '21:00', to: '05:01', every: 30, enabled: true, mode: 'both' });

// Validation
eq(Logic.modelProblems(Logic.blankModel(d)), ['Bitte einen Text eingeben.']);
const many = Array.from({ length: 21 }, () => Object.assign(Logic.blankModel(d), { text: 'x' }));
eq(Logic.listProblems(many)[0].index, -1);
const bad = Logic.modelFromStored({ text: 'x', from: '08:00', to: '09:00', every: 30, repeat: 0, mode: 'both' }, d);
eq(Logic.modelProblems(bad), ['Wie oft sprechen: 1 bis 10 mal.']);
eq(Logic.parseBackup('nope', d).error, 'Das ist keine gültige Sicherung (kein JSON).');
eq(Logic.parseBackup('[{"text":"","from":"08:00","to":"08:01","every":1}]', d).error, 'Erinnerung 1: Bitte einen Text eingeben.');
eq(Logic.parseBackup(Logic.backupJson([m], d), d).models.length, 1);

// App messages in German
eq(Logic.translateAppMessage('schedule[2]: text 1 to 500 characters'), 'Erinnerung 3: Der Text muss 1 bis 500 Zeichen lang sein.');
eq(Logic.translateAppMessage('schedule[0]: repeat 1 to 10'), 'Erinnerung 1: Wie oft sprechen: erlaubt sind 1 bis 10.');
eq(Logic.translateAppMessage('footer_seconds: 1 to 60; speech: expected on or off; ', { footer_seconds: 'Zeile unten' }),
  'Zeile unten: erlaubt sind 1 bis 60. speech: bitte Ein oder Aus.');
eq(Logic.translateAppMessage('something new'), 'Der Fernseher meldet: something new');
eq(Logic.translateAppMessage(null), '');

// MQTT codec: packets split at every byte boundary and glued together
const b = Mqtt.build;
const pub = b.publish({ topic: 'a/b', payload: 'Grüße ' + 'x'.repeat(300), qos: 1, id: 7, retain: true });
eq(Array.from(pub.slice(0, 3)), [0x33, 0xbb, 0x02]); // remaining length 315 in two bytes
const stream = new Uint8Array([...b.ack(2, 0), ...pub, ...b.pingreq()]);
const p = new Mqtt.Parser();
const got = [];
for (const byte of stream) got.push(...p.push(Uint8Array.of(byte)));
eq(got.map(x => x.type), [2, 3, 12]);
const dp = Mqtt.decodePublish(got[1]);
eq([dp.topic, dp.qos, dp.retain, dp.id, Mqtt.text(dp.payload).slice(0, 6)], ['a/b', 1, true, 7, 'Grüße ']);
const p2 = new Mqtt.Parser();
eq(p2.push(stream).length, 3);
// Binary garbage in a payload does not throw
eq(Mqtt.text(Uint8Array.of(0xff, 0xfe, 0x41)).endsWith('A'), true);
assert.throws(() => new Mqtt.Parser().push(Uint8Array.of(0x30, 0xff, 0xff, 0xff, 0xff, 0x01)));
const c = b.connect({ clientId: 'id', username: 'u', password: 'p', keepAlive: 60 });
eq(Array.from(c), [0x10, 20, 0, 4, 77, 81, 84, 84, 4, 0xc2, 0, 60, 0, 2, 105, 100, 0, 1, 117, 0, 1, 112]);
eq(Array.from(b.subscribe(1, [{ topic: 't', qos: 1 }])), [0x82, 6, 0, 1, 0, 1, 116, 1]);
eq(Array.from(b.ack(6, 5)), [0x62, 2, 0, 5]);

// Normalized comparison
const dd = Logic.presDefaults({ message_mode: 'text', message_seconds: 0, speech_repeat: 1, speech_pause: 5 });
const ea = { id: 'x', text: 'Hallo', from: '8:00', to: '08:01', every: 1, enabled: true };
eq(Logic.sameSchedule([ea], [{ every: '1', to: '08:01', from: '08:00', text: 'Hallo ', id: 'x', mode: 'text', show_seconds: 0, repeat: 1 }], dd), true);
eq(Logic.sameSchedule([ea], [{ ...ea, text: 'Hallo!' }], dd), false);
eq(Logic.sameSchedule([{ ...ea, speak: true }], [{ ...ea, mode: 'both' }], dd), true);
eq(Logic.sameSchedule([{ ...ea, mode: 'both', repeat: 2 }], [{ ...ea, mode: 'both' }], dd), false);
eq(Logic.sameSchedule([ea, ea], [ea], dd), false);
eq(Logic.sameSettings({ footer: true, footer_seconds: 6 }, { footer: 'on', footer_seconds: '6', clock: false }), true);
eq(Logic.sameSettings({ footer: true, new_key: 1 }, { footer: true }), true);
eq(Logic.sameSettings({ speech_rate: '-5%' }, { speech_rate: '+0%' }), false);

// What is sent: invalid reminders as their last valid version, never-valid ones not at all
{
  const good = Logic.modelFromStored({ text: 'Trinken', from: '08:00', to: '20:01', every: 60 }, dd);
  const broken = Logic.modelFromStored({ id: 'kaffee', text: 'Kaffee', from: '15:00', to: '15:01', every: 1 }, dd);
  const fresh = Logic.blankModel(dd);
  const first = Logic.scheduleToSend([good, broken], new Map(), dd);
  eq(first.entries.map(e => e.id), ['trinken', 'kaffee']);
  broken.text = '';
  const second = Logic.scheduleToSend([good, broken, fresh], first.lastValid, dd);
  eq(second.entries.map(e => [e.id, e.text]), [['trinken', 'Trinken'], ['kaffee', 'Kaffee']]);
  eq(fresh.id, null);
  fresh.text = 'Trinken';
  eq(Logic.scheduleToSend([good, broken, fresh], second.lastValid, dd).entries.map(e => e.id), ['trinken', 'kaffee', 'trinken-2']);
}

// Offline since when (the TV's status and telemetry as published on 2026-10-26)
{
  const now = new Date('2026-10-26T22:06:30Z');
  eq(Logic.offlineInfo({ state: 'offline', reason: 'power off', time: '2026-10-26T22:03:10Z' }, null, now),
    { head: 'offline seit 23:03 (vor 3 Minuten)', sub: 'ausgeschaltet' });
  eq(Logic.offlineInfo({ state: 'offline', reason: 'power off' }, { time: '2026-10-26T22:01:27Z' }, now),
    { head: 'offline, zuletzt gesehen um 23:01 (vor 5 Minuten)', sub: 'ausgeschaltet' });
  eq(Logic.offlineInfo({ state: 'offline', reason: 'connection lost' }, { time: '2026-10-24T08:00:00Z' }, now).head,
    'offline, zuletzt gesehen um 24.10.2026 10:00 (vor 2 Tagen)');
  eq(Logic.offlineInfo({ state: 'offline', reason: 'connection lost' }, { time: null }, now),
    { head: 'offline', sub: 'Verbindung verloren (Strom aus, Absturz oder kein Internet)' });
  eq(Logic.offlineInfo({ state: 'offline', time: 'kaputt' }, {}, now), { head: 'offline', sub: '' });
  eq(Logic.relativeTime(30000), 'gerade eben');
  eq(Logic.relativeTime(61 * 60000), 'vor 1 Stunde');
  eq(Logic.stopText('reboot (userrequested)'), 'Neustart');
}

// Remembered message inputs
eq(Logic.messageDraft(null), { text: '', pres: { mode: 'both', keyOnly: true, seconds: 30, repeat: 1, pause: 5 } });
eq(Logic.messageDraft(JSON.parse(JSON.stringify({ text: 'Hallo', pres: { mode: 'speech', keyOnly: false, seconds: 12, repeat: NaN, pause: 70 } }))),
  { text: 'Hallo', pres: { mode: 'speech', keyOnly: false, seconds: 12, repeat: 1, pause: 5 } });
eq(Logic.messageDraft({ text: 5, pres: { mode: 'loud' } }).pres.mode, 'both');

// Stream errors: translation
eq(Logic.streamCodeText('ERROR_CODE_IO_NETWORK_CONNECTION_FAILED', ''), { code: 'ERROR_CODE_IO_NETWORK_CONNECTION_FAILED', text: 'Der Sender antwortet nicht', hint: 'Internetverbindung des Fernsehers prüfen.' });
eq(Logic.streamCodeText('ERROR_CODE_IO_BAD_HTTP_STATUS', 'ERROR_CODE_IO_BAD_HTTP_STATUS: InvalidResponseCodeException: Response code: 404').text, 'Die Senderadresse wurde nicht gefunden (404)');
eq(Logic.streamCodeText('ERROR_CODE_IO_BAD_HTTP_STATUS', 'Response code: 403').hint, 'Adresse stimmt nicht oder der Sender sperrt diesen Standort.');
eq(Logic.streamCodeText(2002).code, 'ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT');
eq(Logic.streamCodeText('ERROR_CODE_PARSING_MANIFEST_MALFORMED').text, 'Der Sender liefert unlesbare Daten');
eq(Logic.streamCodeText('ended').text, 'Der Sender hat die Übertragung beendet');
eq(Logic.streamCodeText('ERROR_CODE_SOMETHING_NEW'), { code: 'ERROR_CODE_SOMETHING_NEW', text: 'ERROR_CODE_SOMETHING_NEW', hint: '' });
eq(Logic.shortUrl('https://daserste-live.ard-mcdn.de/daserste/live/hls/de/master.m3u8'), 'daserste-live.ard-mcdn.de/.../master.m3u8');
// Stream errors: source selection
{
  const url = 'https://a.example/live/master.m3u8';
  const red = { channel: 'ARD', picture: false, screen: 'red-no-picture', stream_error: '' };
  const ev = (time, code, extra = {}) => ({ time, type: 'stream', channel: 'ARD', url, code, message: code + ': cause', ...extra });
  eq(Logic.streamProblem({ ...red, picture: true, screen: 'ok' }, { url, last_stream_error: ev('2026-10-26T22:00:00Z', 'ended') }, null), null);
  const sp = Logic.streamProblem(red, { url, last_stream_error: ev('2026-10-26T22:00:00Z', 'ERROR_CODE_IO_NETWORK_CONNECTION_FAILED', { attempt: 4 }),
    last_error: { time: '2026-10-26T22:01:00Z', type: 'no-picture', url } }, null);
  eq([sp.head, sp.raw, sp.channel, sp.url], ['Der Sender antwortet nicht (ERROR_CODE_IO_NETWORK_CONNECTION_FAILED, Versuch 4)',
    'ERROR_CODE_IO_NETWORK_CONNECTION_FAILED: cause', 'ARD', url]);
  // newest wins; another address does not count
  eq(Logic.streamProblem(red, { url, last_stream_error: ev('2026-10-26T22:00:00Z', 'ended') }, ev('2026-10-26T22:05:00Z', 'ERROR_CODE_IO_FILE_NOT_FOUND')).code, 'ERROR_CODE_IO_FILE_NOT_FOUND');
  eq(Logic.streamProblem(red, { url, last_stream_error: ev('2026-10-26T22:00:00Z', 'ended') }, ev('2026-10-26T22:05:00Z', 'ERROR_CODE_IO_FILE_NOT_FOUND', { url: 'https://other/x' })).code, 'ended');
  // older app: last_error of type stream, or the live event
  eq(Logic.streamProblem(red, { url, last_error: ev('2026-10-26T22:00:00Z', 'ERROR_CODE_DECODING_FAILED') }, null).code, 'ERROR_CODE_DECODING_FAILED');
  // nothing known: the state's short code, or what the screen says
  eq(Logic.streamProblem({ ...red, stream_error: 'ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT' }, { url }, null).head, 'Der Sender antwortet zu langsam (ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT)');
  eq(Logic.streamProblem({ ...red, screen: 'yellow-no-internet' }, null, null).title, 'Der Fernseher hat kein Internet');
  eq(Logic.streamProblem({ channel: 'ARD', picture: false, screen: 'no-picture-yet' }, { url }, null), null);
  eq(Logic.streamProblem(null, null, null), null);
}

// Sync state machine with a fake clock and a fake transport
function fakeClock() {
  let t = 0, id = 0;
  const timers = new Map();
  return {
    set(fn, ms) { timers.set(++id, { at: t + ms, fn }); return id; },
    clear(i) { timers.delete(i); },
    async advance(ms) {
      const end = t + ms;
      for (;;) {
        let next = null;
        for (const [i, x] of timers) if (x.at <= end && (!next || x.at < next[1].at)) next = [i, x];
        if (!next) break;
        timers.delete(next[0]);
        t = next[1].at;
        next[1].fn();
        await settle();
      }
      t = end;
    },
  };
}
const settle = () => new Promise(r => setImmediate(r));
function channel(clock, opts = {}) {
  const c = { sent: [], adopted: [] };
  c.ch = new Sync.Channel({ clock, same: (a, b) => JSON.stringify(a) === JSON.stringify(b),
    send: (v, retain) => { c.sent.push({ v, retain }); return opts.fail ? Promise.reject(new Error('x')) : Promise.resolve(); },
    onAdopt: v => c.adopted.push(v) });
  return c;
}
(async () => {
  // Load: adopt the TV's state, send nothing
  let clock = fakeClock(), c = channel(clock);
  c.ch.link(true, true);
  c.ch.fromTv(['a']);
  eq([c.ch.status, c.adopted, c.sent.length], ['confirmed', [['a']], 0]);
  // An edit: one publish of the full state, not retained while the TV is online; the TV's echo confirms it
  c.ch.edit(['a', 'b']);
  await settle();
  eq([c.sent, c.ch.status], [[{ v: ['a', 'b'], retain: false }], 'pending']);
  c.ch.fromCommand(['a', 'b']); // own echo
  c.ch.fromTv(['a', 'b']);
  eq([c.ch.status, c.ch.owner, c.adopted.length], ['confirmed', null, 1]);
  // Two quick edits: two publishes, each with the full state
  c.ch.edit(['a']); c.ch.edit(['a', 'c']);
  eq(c.sent.slice(1).map(x => x.v), [['a'], ['a', 'c']]);
  // An edit back to the same value sends nothing
  c.ch.edit(['a', 'c']);
  eq(c.sent.length, 3);
  // No answer: 3 retries with backoff, then an error
  await settle();
  await clock.advance(15000);
  eq([c.ch.status, c.ch.message, c.sent.length], ['pending', 'retry', 3]);
  await clock.advance(2000);
  eq(c.sent.length, 4);
  await clock.advance(15000 + 4000 + 15000 + 8000);
  eq(c.sent.length, 6);
  await clock.advance(15000);
  eq([c.ch.status, c.ch.message, c.sent.length], ['error', 'no-answer', 6]);
  // The TV refuses: error with its message, no retries
  clock = fakeClock(); c = channel(clock);
  c.ch.link(true, true); c.ch.fromTv([]);
  c.ch.edit(['bad']);
  await settle();
  c.ch.fromResult(false, 'schedule[0]: text 1 to 500 characters');
  await clock.advance(60000);
  eq([c.ch.status, c.ch.message, c.sent.length], ['error', 'schedule[0]: text 1 to 500 characters', 1]);
  // TV offline: sent retained, waits; the TV applies it when it is back
  clock = fakeClock(); c = channel(clock);
  c.ch.link(true, false); c.ch.fromTv(['old']);
  c.ch.edit(['new']);
  await settle();
  await clock.advance(60000);
  eq([c.sent, c.ch.status, c.ch.message], [[{ v: ['new'], retain: true }], 'offline-wait', 'tv']);
  c.ch.link(true, true);
  eq(c.ch.status, 'pending');
  c.ch.fromTv(['new']);
  eq([c.ch.status, c.sent.length], ['confirmed', 1]);
  // The TV goes away before confirming: sent again, retained
  c.ch.edit(['newer']);
  c.ch.link(true, false);
  eq(c.sent.slice(1), [{ v: ['newer'], retain: false }, { v: ['newer'], retain: true }]);
  // Not connected: kept, sent at the reconnect
  clock = fakeClock(); c = channel(clock);
  c.ch.link(true, true); c.ch.fromTv([]);
  c.ch.link(false, true);
  c.ch.edit(['x']);
  eq([c.sent.length, c.ch.status, c.ch.message], [0, 'offline-wait', 'connection']);
  c.ch.link(true, true);
  eq(c.sent.map(x => x.v), [['x']]);
  // A failed publish waits for the connection
  clock = fakeClock(); c = channel(clock, { fail: true });
  c.ch.link(true, true); c.ch.fromTv([]);
  c.ch.edit(['y']);
  await settle();
  eq([c.ch.status, c.ch.message], ['offline-wait', 'connection']);
  // Incoming changes: adopted when there is nothing local, ignored while typing or unconfirmed
  clock = fakeClock(); c = channel(clock);
  c.ch.link(true, true); c.ch.fromTv(['a']);
  c.ch.fromTv(['b']);
  eq([c.adopted.pop(), c.sent.length], [['b'], 0]);
  c.ch.touch();
  c.ch.fromTv(['c']);
  c.ch.fromCommand(['d']);
  eq([c.ch.desired, c.adopted.length], [['b'], 1]);
  c.ch.edit(['b2']);
  c.ch.fromTv(['c']);
  eq([c.ch.desired, c.ch.status], [['b2'], 'pending']);
  // Another editor's command while the TV is offline: shown at once, waits for the TV
  clock = fakeClock(); c = channel(clock);
  c.ch.link(true, false); c.ch.fromTv(['a']);
  c.ch.fromCommand(['other']);
  eq([c.ch.owner, c.ch.status, c.adopted.pop(), c.sent.length], ['other', 'offline-wait', ['other'], 0]);
  c.ch.fromTv(['a']); // the TV's old state must not undo it
  eq(c.ch.desired, ['other']);
  c.ch.link(true, true);
  c.ch.fromTv(['other']);
  eq([c.ch.owner, c.ch.status], [null, 'confirmed']);
  // Two pages and a TV: each page adopts the other's edit and nothing bounces back
  clock = fakeClock();
  const pages = [channel(clock), channel(clock)];
  const tv = { value: [] };
  const deliver = () => {
    for (const p of pages) {
      while (p.sent.length > p.delivered) {
        const v = p.sent[p.delivered++].v;
        for (const q of pages) q.ch.fromCommand(v);
        tv.value = v;
        for (const q of pages) q.ch.fromTv(v);
      }
    }
  };
  for (const p of pages) { p.delivered = 0; p.ch.link(true, true); p.ch.fromTv(tv.value); }
  pages[0].ch.edit(['von A']); deliver();
  pages[1].ch.edit(['von A', 'von B']); deliver();
  await clock.advance(120000); deliver();
  eq([pages[0].sent.length, pages[1].sent.length], [1, 1]);
  eq([pages[0].ch.desired, pages[1].ch.desired, pages[0].ch.status, pages[1].ch.status],
    [['von A', 'von B'], ['von A', 'von B'], 'confirmed', 'confirmed']);
  console.log(`ok - ${n} checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
