"""Protocol 9 integration checks, optionally seeded by the original Go server."""
import asyncio
from contextlib import asynccontextmanager
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
import uuid

from websockets.asyncio.client import connect

MAP = "/Game/Maps/Hotel"
BUILD = "a" * 40
ALICE = uuid.uuid4().hex
BOB = uuid.uuid4().hex
ALICE_SESSION = uuid.uuid4().hex
BLOB = b"Editor Collaboration integration blob"
HASH = hashlib.md5(BLOB).hexdigest()


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(base, path, token=None, content=None, extra=None):
    headers = dict(extra or {})
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(
        base + path, data=content, headers=headers, method="PUT" if content is not None else "GET"
    )
    try:
        with urllib.request.urlopen(req, timeout=3) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


class Client:
    def __init__(self, socket):
        self.socket = socket

    async def send(self, event, **fields):
        await self.socket.send(json.dumps({"type": event, **fields}))

    async def expect(self, event):
        async with asyncio.timeout(5):
            while True:
                msg = json.loads(await self.socket.recv())
                if msg["type"] == event:
                    return msg
                if msg["type"] in {"error", "contentConflict"}:
                    raise AssertionError((event, msg))

    async def join(self, author, session, name, room="Hotel", password="hotel-key",
                   commit=BUILD, protocol=9, expected="welcome"):
        await self.send("hello", protocolVersion=protocol, clientId=author, session=session,
                        engineVersion="5.8.0", commit=commit, room=room, password=password,
                        state={"name": name, "color": "#112233", "private": "drop"})
        return await self.expect(expected)


@asynccontextmanager
async def client(base):
    async with connect(base.replace("http://", "ws://") + "/ws") as sock:
        yield Client(sock)


@asynccontextmanager
async def server(executable, root, data, rooms, tag):
    port = available_port()
    address = f"127.0.0.1:{port}"
    base = f"http://{address}"
    cmd = [str(executable), "-addr", address, "-data", str(data),
           "-room-passwords", str(rooms), "-no-tray"]
    env = {key: value for key, value in os.environ.items() if not key.startswith("COLLAB_")}
    env["COLLAB_UPDATE_CHECK"] = "0"
    hidden = getattr(subprocess, "CREATE_NO_WINDOW", 0)
    logs = root / (tag + ".log")
    with logs.open("wb") as log:
        proc = subprocess.Popen(cmd, env=env, creationflags=hidden, stdout=log, stderr=log)
        try:
            for _ in range(100):
                assert proc.poll() is None, proc.returncode
                try:
                    status, data_bytes = request(base, "/health")
                    if status == 200:
                        assert json.loads(data_bytes)["protocolVersion"] == 9
                        break
                except OSError:
                    pass
                await asyncio.sleep(0.05)
            else:
                raise AssertionError("server startup timed out")
            yield base
        except BaseException:
            print(logs.read_text(encoding="utf-8", errors="replace"), file=sys.stderr)
            raise
        finally:
            if proc.poll() is None:
                try:
                    subprocess.run([*cmd, "-stop"], env=env, creationflags=hidden,
                                   stdout=log, stderr=log, timeout=15, check=True)
                    proc.wait(timeout=8)
                finally:
                    if proc.poll() is None:
                        proc.kill()
                        proc.wait()
        assert proc.returncode == 0


async def exercise(base):
    async with client(base) as wrong:
        rejection = await wrong.join(ALICE, ALICE_SESSION, "Alice", password="bad", expected="error")
        assert rejection["code"] == "auth"

    async with client(base) as alice, client(base) as bob, client(base) as stranger:
        welcome = await alice.join(ALICE, ALICE_SESSION, "Alice")
        author, token = welcome["authorId"], welcome["blobToken"]
        await bob.join(BOB, uuid.uuid4().hex, "Bob")
        roster = await bob.expect("roster")
        assert roster["peers"][0]["state"] == {"name":"Alice","color":"#112233"}
        other = await stranger.join(uuid.uuid4().hex, uuid.uuid4().hex, "Other",
                                    room="Other", password="other-key")
        other_token = other["blobToken"]

        await alice.send("chat", text=" Hello ")
        chat = (await bob.expect("chat"))["message"]
        assert chat["authorId"] == author and chat["text"] == "Hello"
        await alice.send("chat.history", before=chat["id"])
        history = await alice.expect("chatOlder")
        assert [message["text"] for message in history["messages"]] == ["Imported"] and history["complete"]

        anchor = {"kind":"map","map":MAP,"x":1}
        await alice.send("comment.create", anchor=anchor, text="Review")
        comment = (await bob.expect("comment"))["comment"]
        cid, mid = comment["id"], comment["messages"][0]["id"]
        await bob.send("comment.edit", id=cid, messageId=mid, text="Rejected")
        assert (await bob.expect("error"))["reason"] == "notMessageAuthor"
        await bob.send("comment.reply", id=cid, text="Seen")
        assert len((await bob.expect("comment"))["comment"]["messages"]) == 2
        await alice.send("comment.edit", id=cid, messageId=mid, text="Edited")
        edited = (await bob.expect("comment"))["comment"]["messages"][0]
        assert edited["text"] == "Edited" and edited["editedAt"] >= edited["at"]
        await alice.send("comment.move", id=cid, anchor={**anchor,"x":2})
        assert (await bob.expect("comment"))["comment"]["anchor"]["x"] == 2
        await alice.send("comment.resolve", id=cid, resolved=True)
        assert (await bob.expect("comment"))["comment"]["resolved"]
        await alice.send("ping", anchor=anchor, seconds=1)
        assert (await bob.expect("ping"))["seconds"] == 15

        await alice.send("asset.modify", package=MAP, hash="FF", dirty=True, working=True)
        record = (await bob.expect("assetModified"))["record"]
        assert record["working"] and record["hash"] == "ff"
        await alice.send("presence", state={"name":"Alicia","cam":None,"private":42})
        while (presence := await bob.expect("presence"))["state"].get("name") != "Alicia":
            pass
        assert presence["state"] == {"name":"Alicia","cam":None}
        assert (await bob.expect("assetCleared"))["author"] == "Alice"
        assert (await bob.expect("assetModified"))["record"]["author"] == "Alicia"

        path = "/blob/" + HASH
        assert request(base, path, token, b"wrong")[0] == 400
        assert request(base, path, token, BLOB)[0] == 200
        assert request(base, path, other_token)[0] == 404
        assert request(base, path, token) == (200, BLOB)
        assert request(base, path, token, extra={"Range":"bytes=1-3"}) == (206, BLOB[1:4])

        payload = [{"op":"set","actor":"Door","blob":HASH}]
        await alice.send("ops.preview", target=MAP, revision=0, ops=payload)
        assert (await bob.expect("opsPreview"))["revision"] == 0
        await alice.send("ops", target=MAP, kind="level", base=BUILD, revision=0, ops=payload)
        event = await bob.expect("opsApplied")
        assert event["seq"] == 1 and event["senderId"] == welcome["selfId"]
        await alice.send("graph.resync", target=MAP, graph="Graph", revision=0)
        assert (await bob.expect("graphResync"))["graph"] == "Graph"
        await bob.send("graph.snapshot", target=MAP, graph="Graph", revision=0, text="graph text")
        assert (await bob.expect("graphSnapshot"))["text"] == "graph text"

        await bob.send("ops.sync", target=MAP, since=0, revision=0)
        assert (await bob.expect("opsBatch"))["entries"][0]["ops"] == payload
        await bob.send("pkg.publish", package=MAP, hash=HASH, size=0, base=BUILD, since=1)
        assert (await bob.expect("error"))["reason"] == "missingBlob"
        await alice.send("pkg.publish", package=MAP, hash=HASH, size=len(BLOB), base=BUILD, since=1)
        publish = await bob.expect("pkgPublished")
        assert publish["record"]["seq"] == 1 and publish["record"]["cutoff"] == 1
        await alice.send("ops", target=MAP, kind="level", base=BUILD, revision=0, ops=payload)
        assert (await alice.expect("contentConflict"))["target"] == MAP
        await bob.send("ops", target=MAP, kind="level", base=BUILD, revision=1, ops=payload)
        assert (await bob.expect("opsApplied"))["seq"] == 2

        async with client(base) as replacement:
            new = await replacement.join(ALICE, ALICE_SESSION, "Alicia")
            assert new["authorId"] == author and new["selfId"] != welcome["selfId"]
            assert request(base, path, token)[0] == 401
            await replacement.socket.close()
        clear = (await bob.expect("assetModified"))["record"]
        assert not clear["working"] and clear["authorId"] == author


async def verify_restart(base):
    async with client(base) as alice:
        welcome = await alice.join(ALICE, uuid.uuid4().hex, "Alicia")
        assert (await alice.expect("chatHistory"))["messages"][-1]["text"] == "Hello"
        comment = (await alice.expect("comments"))["comments"][0]
        assert comment["resolved"] and comment["messages"][0]["text"] == "Edited"
        record = (await alice.expect("assets"))["records"][0]
        assert not record["working"] and record["authorId"] == welcome["authorId"]
        package = (await alice.expect("packages"))["records"][0]
        assert package["hash"] == HASH and package["cutoff"] == 1
        assert request(base, "/blob/"+HASH, welcome["blobToken"]) == (200, BLOB)
        await alice.send("ops.sync", target=MAP, since=1, revision=1)
        assert (await alice.expect("opsBatch"))["entries"][0]["seq"] == 2

        async with client(base) as mismatched:
            await mismatched.join(BOB, uuid.uuid4().hex, "Bob", commit="another")
            assert (await mismatched.expect("notice"))["code"] == "commit"
            assert not (await mismatched.expect("packages"))["records"]
        assert (await alice.expect("notice"))["code"] == "commit"
        assert (await alice.expect("notice"))["code"] == "compatible"
        assert (await alice.expect("packages"))["records"]

        await alice.send("asset.clear", package=MAP)
        assert (await alice.expect("assetCleared"))["package"] == MAP
        await alice.send("comment.delete", id=comment["id"])
        assert (await alice.expect("commentDeleted"))["id"] == comment["id"]
        await alice.send("pkg.delete", package=MAP, base=BUILD, revision=1)
        deleted = await alice.expect("pkgDeleted")
        assert deleted["revision"] == 2 and deleted["cutoff"] == 2
        await alice.send("ops.sync", target=MAP, since=0, revision=1)
        assert (await alice.expect("pkgDeleted"))["revision"] == 2


async def verify_convergence(base, suffix):
    target = "/Game/Concurrent" + suffix
    renamed = target + "Renamed"
    from_git = "/Game/Unpublished" + suffix
    author = uuid.uuid4().hex
    operation_a = uuid.uuid4().hex
    operation_b = uuid.uuid4().hex
    async with client(base) as alice, client(base) as bob:
        welcome = await alice.join(author, uuid.uuid4().hex, "Alice")
        await bob.join(uuid.uuid4().hex, uuid.uuid4().hex, "Bob")
        await alice.expect("packages")
        await bob.expect("packages")
        common = dict(target=target, kind="level", base=BUILD, revision=0)
        await alice.send("ops", **common, operationId=operation_a, ops=[{"op":"set","value":1}])
        await bob.send("ops", **common, operationId=operation_b, ops=[{"op":"set","value":2}])
        await alice.socket.close()
        # Bob observes both commits while Alice leaves her acknowledgements unread.
        b_entries = [await bob.expect("opsApplied"), await bob.expect("opsApplied")]
        assert [entry["seq"] for entry in b_entries] == [1, 2]
        expected = b_entries[-1]["ops"][0]["value"]

        async with client(base) as reconnect:
            welcome = await reconnect.join(author, uuid.uuid4().hex, "Alice")
            await reconnect.expect("packages")
            await reconnect.send("ops", **common, operationId=operation_a, ops=[{"op":"set","value":1}])
            ack = await reconnect.expect("opsAck")
            original = next(entry for entry in b_entries if entry["operationId"] == operation_a)
            assert ack["seq"] == original["seq"]
            await reconnect.send("ops.sync", target=target, since=0, revision=0)
            journal = (await reconnect.expect("opsBatch"))["entries"]
            assert [entry["operationId"] for entry in journal] == [entry["operationId"] for entry in b_entries]
            assert journal[-1]["ops"][0]["value"] == expected

            assert request(base, "/blob/" + HASH, welcome["blobToken"], BLOB)[0] == 200
            await reconnect.send("pkg.publish", package=target, hash=HASH, size=len(BLOB), base=BUILD, since=2)
            assert (await reconnect.expect("pkgPublished"))["record"]["cutoff"] == 2
            await reconnect.send("ops", **common, operationId=operation_a, ops=[{"op":"set","value":1}])
            assert (await reconnect.expect("opsAck"))["seq"] == original["seq"]
            await reconnect.send("pkg.delete", package=from_git, base=BUILD, revision=0)
            assert (await reconnect.expect("pkgDeleted"))["package"] == from_git
            await reconnect.send("pkg.publish", package=renamed, hash=HASH, size=len(BLOB),
                                 base=BUILD, **{"from":target,"fromRevision":0,"fromSince":2})
            assert (await reconnect.expect("contentConflict"))["target"] == renamed
            await reconnect.send("pkg.publish", package=renamed, hash=HASH, size=len(BLOB),
                                 base=BUILD, **{"from":target,"fromRevision":1,"fromSince":1})
            assert (await reconnect.expect("contentConflict"))["target"] == renamed
            await reconnect.send("pkg.publish", package=renamed, hash=HASH, size=len(BLOB),
                                 base=BUILD, **{"from":target,"fromRevision":1,"fromSince":2})
            assert (await reconnect.expect("pkgPublished"))["record"]["package"] == renamed
            removal = await reconnect.expect("pkgDeleted")
            assert removal["package"] == target and removal["to"] == renamed and removal["revision"] == 2

    await verify_removals(base, target, from_git)
    return author, operation_a, original["seq"]


async def verify_removals(base, target, from_git):
    renamed = target + "Renamed"
    async with client(base) as late:
        await late.join(uuid.uuid4().hex, uuid.uuid4().hex, "Late")
        snapshot = await late.expect("packages")
        assert renamed in {record["package"] for record in snapshot["records"]}
        assert target not in {record["package"] for record in snapshot["records"]}
        removals = {record["package"]:record for record in snapshot["removals"]}
        assert removals[target]["to"] == renamed and from_git in removals
        await late.send("ops.sync", target=target, since=0, revision=0)
        assert (await late.expect("pkgDeleted"))["to"] == renamed


async def main(rust, go=None):
    with tempfile.TemporaryDirectory(prefix="collab-rust-protocol-") as temp:
        root = Path(temp)
        rooms = root / "rooms.json"
        rooms.write_text(json.dumps({"Hotel":"hotel-key","Other":"other-key"}), encoding="utf-8")
        data = root / "data"
        data.mkdir()
        (data / "room-legacy.json").write_text(json.dumps({"room":"Hotel", "comments":None, "assets":None, "chat":[
            {"id":"legacy","author":"Legacy","color":"","text":"Imported","at":1}]}), encoding="utf-8")
        async with server(go or rust, root, data, rooms, "seed") as base:
            await exercise(base)
            author, operation, sequence = await verify_convergence(base, "Seed")
        assert (data / "room-legacy.json.imported").exists()
        async with server(rust, root, data, rooms, "restart") as base:
            await verify_restart(base)
            await verify_removals(base, "/Game/ConcurrentSeed", "/Game/UnpublishedSeed")
            async with client(base) as reconnect:
                await reconnect.join(author, uuid.uuid4().hex, "Alice")
                await reconnect.send("ops", target="/Game/ConcurrentSeed", kind="level", base=BUILD,
                                     revision=0, operationId=operation, ops=[{"op":"set","value":1}])
                assert (await reconnect.expect("opsAck"))["seq"] == sequence
            await verify_convergence(base, "Restarted")
        print("Protocol 9, blob transfers and persistent restart passed.")


if __name__ == "__main__":
    if len(sys.argv) not in (2,3):
        sys.exit("Usage: python tests/protocol.py <rust-server> [go-server]")
    asyncio.run(main(*(Path(arg).resolve(strict=True) for arg in sys.argv[1:])))
