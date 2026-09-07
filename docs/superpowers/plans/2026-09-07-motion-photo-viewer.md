# Motion Photo Viewer per Flickr Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CLI Python che scansiona le foto pubbliche Flickr dell'utente, rileva le Motion Photo Pixel, estrae il video incorporato, pubblica una pagina viewer statica (foto + video, stile Flickr photo page) e linka quella pagina nella descrizione Flickr della foto.

**Architecture:** Moduli puri e testabili in isolamento (detection, oauth signing, client Flickr, generazione pagina, upload video, publish git) composti da un orchestratore CLI (`scanner.py`). Stato persistito come machine tag Flickr (`flickrmp:status=...`), nessun database locale. Auth OAuth 1.0a solo lato CLI; la pagina pubblicata (GitHub Pages) e il video (Cloudflare R2) restano pubblici senza auth.

**Tech Stack:** Python 3.12 stdlib (urllib, hmac, hashlib per OAuth 1.0a — nessuna libreria OAuth esterna), boto3 (client S3-compatibile per Cloudflare R2), pytest, git CLI, gh CLI (GitHub).

**Spec:** `docs/superpowers/specs/2026-09-07-motion-photo-viewer-design.md`

## Global Constraints

- Nessun file di stato locale: lo stato di scansione vive esclusivamente come machine tag Flickr `flickrmp:status={checked|published|lost}` (namespace `flickrmp`, predicate `status`).
- Ogni scrittura verso Flickr/R2/git deve essere idempotente: un rerun sullo stesso batch non deve duplicare link in descrizione, non deve ricaricare il video se già presente, non deve aggiungere un secondo tag di stato.
- Il tag di stato va scritto **per ultimo**, dopo che descrizione e pagina sono state pubblicate con successo (garanzia di non lasciare stato incoerente in caso di crash a metà run).
- Download dell'originale Flickr richiede sempre OAuth owner-autenticato (validato: chiamata anonima ha `candownload: 0`).
- Estrazione video: il match testuale `ftyp` è 4 byte dopo l'inizio reale del box MP4 (i 4 byte precedenti sono la box-size). L'estrazione parte da `offset_del_match - 4`.
- Nessuna libreria OAuth esterna: l'algoritmo di firma HMAC-SHA1 è già implementato e validato contro il vettore canonico OAuth 1.0 (`tR3+Ty81lMeYAr/Fid0kMTYa/WM=`) e contro l'API Flickr reale durante lo spike.
- Secrets via variabili d'ambiente, caricate da Bitwarden "Personal Secrets" seguendo il pattern già in uso su questa macchina (vedi `~/CLAUDE.md`, funzione `bw-unlock`).

---

## File Structure

```
flickr-motion-photos/
├── requirements.txt
├── config.py            # carica configurazione da env vars
├── motion_detect.py      # rilevamento motion photo + estrazione video (puro, no I/O)
├── flickr_oauth.py       # firma OAuth 1.0a, request/access token, chiamata HTTP firmata
├── flickr_client.py      # metodi Flickr API di alto livello usati dallo scanner
├── site_generator.py     # rendering pagina viewer da template
├── site/
│   └── template.html     # template HTML pagina viewer (stile dark, tipo Flickr photo page)
├── r2_upload.py          # upload video su Cloudflare R2 (S3-compatibile)
├── git_publish.py        # commit + push delle pagine generate
├── scanner.py            # CLI orchestratore
├── authorize.py          # script one-off per ottenere il token OAuth
└── tests/
    ├── test_motion_detect.py
    ├── test_flickr_oauth.py
    ├── test_flickr_client.py
    ├── test_site_generator.py
    ├── test_r2_upload.py
    ├── test_git_publish.py
    └── test_scanner.py
```

---

### Task 1: Motion photo detection

**Files:**
- Create: `motion_detect.py`
- Test: `tests/test_motion_detect.py`

**Interfaces:**
- Produces: `MotionPhotoResult` dataclass (`is_motion: bool`, `video_bytes: Optional[bytes]`, `reason: str` con valori `"published"|"checked"|"lost"`), funzione `detect_motion_photo(jpeg_bytes: bytes) -> MotionPhotoResult`. Usato da Task 7 (scanner.py) e corrisponde 1:1 ai valori del machine tag `flickrmp:status=<reason>`.

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_motion_detect.py
from motion_detect import detect_motion_photo

XMP_MARKER = b'xmlns:GCamera="http://ns.google.com/photos/1.0/camera/" GCamera:MotionPhoto="1"'

def _fake_mp4_payload():
    # box-size (4 byte) + 'ftyp' + resto arbitrario, replica la struttura vista nello spike
    return b'\x00\x00\x00\x18ftypisom\x00\x02\x00\x00isomiso2mp41' + b'FAKEMDATBYTES'

def test_published_when_xmp_and_video_present():
    jpeg = b'JFIF-fake-header' + XMP_MARKER + b'restofjpegdata\xff\xd9' + _fake_mp4_payload()
    result = detect_motion_photo(jpeg)
    assert result.is_motion is True
    assert result.reason == "published"
    assert result.video_bytes.startswith(b'\x00\x00\x00\x18ftypisom')

def test_checked_when_no_motion_markers_at_all():
    jpeg = b'JFIF-fake-header-plain-photo-no-markers\xff\xd9'
    result = detect_motion_photo(jpeg)
    assert result.is_motion is False
    assert result.reason == "checked"
    assert result.video_bytes is None

def test_lost_when_xmp_present_but_video_missing():
    jpeg = b'JFIF-fake-header' + XMP_MARKER + b'restofjpegdata\xff\xd9'
    result = detect_motion_photo(jpeg)
    assert result.is_motion is False
    assert result.reason == "lost"
    assert result.video_bytes is None
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_motion_detect.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'motion_detect'`

- [ ] **Step 3: Write the implementation**

```python
# motion_detect.py
from dataclasses import dataclass
from typing import Optional

XMP_MOTION_MARKER = b'GCamera:MotionPhoto="1"'
MP4_BOX_TYPE_MARKER = b'ftyp'
BOX_SIZE_FIELD_LEN = 4


@dataclass
class MotionPhotoResult:
    is_motion: bool
    video_bytes: Optional[bytes]
    reason: str  # "published" | "checked" | "lost"


def _find_embedded_video(jpeg_bytes: bytes) -> Optional[bytes]:
    idx = jpeg_bytes.find(MP4_BOX_TYPE_MARKER)
    if idx < BOX_SIZE_FIELD_LEN:
        return None
    box_start = idx - BOX_SIZE_FIELD_LEN
    video = jpeg_bytes[box_start:]
    if len(video) < 8:
        return None
    return video


def detect_motion_photo(jpeg_bytes: bytes) -> MotionPhotoResult:
    has_xmp_flag = XMP_MOTION_MARKER in jpeg_bytes
    video = _find_embedded_video(jpeg_bytes)

    if video is not None:
        return MotionPhotoResult(is_motion=True, video_bytes=video, reason="published")
    if has_xmp_flag:
        return MotionPhotoResult(is_motion=False, video_bytes=None, reason="lost")
    return MotionPhotoResult(is_motion=False, video_bytes=None, reason="checked")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_motion_detect.py -v`
Expected: 3 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add motion_detect.py tests/test_motion_detect.py
git commit -m "feat: motion photo detection from JPEG+embedded MP4 bytes"
```

---

### Task 2: OAuth 1.0a signing

**Files:**
- Create: `flickr_oauth.py`
- Test: `tests/test_flickr_oauth.py`

**Interfaces:**
- Produces:
  - `sign(method: str, url: str, params: dict, consumer_secret: str, token_secret: str = "") -> str`
  - `build_oauth_params(consumer_key: str, token: Optional[str] = None) -> dict`
  - `signed_get(url: str, params: dict, consumer_key: str, consumer_secret: str, token: Optional[str] = None, token_secret: str = "") -> str` (esegue la richiesta HTTP e ritorna il body come stringa)
  - `get_request_token(consumer_key: str, consumer_secret: str) -> dict` (`{"oauth_token", "oauth_token_secret"}`)
  - `build_authorize_url(request_token: str, perms: str = "write") -> str`
  - `get_access_token(consumer_key: str, consumer_secret: str, request_token: str, request_token_secret: str, verifier: str) -> dict` (`{"oauth_token", "oauth_token_secret", "user_nsid", "username"}`)
- Consumato da Task 3 (`flickr_client.py`) e da Task 8 (`authorize.py`).

- [ ] **Step 1: Write the failing test**

Il vettore usato sotto è l'esempio canonico OAuth 1.0 (consumer/token/secret/nonce/timestamp fissi), già verificato manualmente contro questa stessa implementazione durante lo spike di design: produce `tR3+Ty81lMeYAr/Fid0kMTYa/WM=`.

```python
# tests/test_flickr_oauth.py
from flickr_oauth import sign, build_oauth_params, build_authorize_url


def test_sign_matches_canonical_oauth1_vector():
    params = {
        'oauth_consumer_key': 'dpf43f3p2l4k3l03',
        'oauth_token': 'nnch734d00sl2jdk',
        'oauth_signature_method': 'HMAC-SHA1',
        'oauth_timestamp': '1191242096',
        'oauth_nonce': 'kllo9940pd9333jh',
        'oauth_version': '1.0',
        'file': 'vacation.jpg',
        'size': 'original',
    }
    sig = sign('GET', 'http://photos.example.net/photos', params, 'kd94hf93k423kf44', 'pfkkdhi9sl3r4s00')
    assert sig == 'tR3+Ty81lMeYAr/Fid0kMTYa/WM='


def test_build_oauth_params_without_token_has_no_token_field():
    p = build_oauth_params('consumer-key-x')
    assert p['oauth_consumer_key'] == 'consumer-key-x'
    assert p['oauth_signature_method'] == 'HMAC-SHA1'
    assert p['oauth_version'] == '1.0'
    assert 'oauth_token' not in p
    assert len(p['oauth_nonce']) == 32


def test_build_oauth_params_with_token_includes_it():
    p = build_oauth_params('consumer-key-x', token='req-token-abc')
    assert p['oauth_token'] == 'req-token-abc'


def test_build_authorize_url_includes_token_and_perms():
    url = build_authorize_url('req-token-123', perms='write')
    assert url == 'https://www.flickr.com/services/oauth/authorize?oauth_token=req-token-123&perms=write'
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_flickr_oauth.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'flickr_oauth'`

- [ ] **Step 3: Write the implementation**

```python
# flickr_oauth.py
import hmac
import hashlib
import base64
import time
import random
import string
import urllib.parse
import urllib.request

REQUEST_TOKEN_URL = 'https://www.flickr.com/services/oauth/request_token'
AUTHORIZE_URL = 'https://www.flickr.com/services/oauth/authorize'
ACCESS_TOKEN_URL = 'https://www.flickr.com/services/oauth/access_token'


def sign(method: str, url: str, params: dict, consumer_secret: str, token_secret: str = "") -> str:
    base_params = "&".join(
        f"{urllib.parse.quote(k, safe='')}={urllib.parse.quote(str(params[k]), safe='')}"
        for k in sorted(params)
    )
    base_string = "&".join([
        method.upper(),
        urllib.parse.quote(url, safe=''),
        urllib.parse.quote(base_params, safe=''),
    ])
    key = f"{urllib.parse.quote(consumer_secret, safe='')}&{urllib.parse.quote(token_secret, safe='')}"
    digest = hmac.new(key.encode(), base_string.encode(), hashlib.sha1).digest()
    return base64.b64encode(digest).decode()


def _nonce(length: int = 32) -> str:
    alphabet = string.ascii_letters + string.digits
    return ''.join(random.choice(alphabet) for _ in range(length))


def build_oauth_params(consumer_key: str, token: str = None) -> dict:
    params = {
        'oauth_nonce': _nonce(),
        'oauth_timestamp': str(int(time.time())),
        'oauth_consumer_key': consumer_key,
        'oauth_signature_method': 'HMAC-SHA1',
        'oauth_version': '1.0',
    }
    if token:
        params['oauth_token'] = token
    return params


def signed_get(url: str, params: dict, consumer_key: str, consumer_secret: str,
                token: str = None, token_secret: str = "") -> str:
    full_params = dict(params)
    full_params['oauth_signature'] = sign('GET', url, params, consumer_secret, token_secret)
    query = urllib.parse.urlencode(full_params)
    with urllib.request.urlopen(f"{url}?{query}") as response:
        return response.read().decode()


def get_request_token(consumer_key: str, consumer_secret: str) -> dict:
    params = build_oauth_params(consumer_key)
    params['oauth_callback'] = 'oob'
    body = signed_get(REQUEST_TOKEN_URL, params, consumer_key, consumer_secret)
    return dict(urllib.parse.parse_qsl(body))


def build_authorize_url(request_token: str, perms: str = "write") -> str:
    return f"{AUTHORIZE_URL}?oauth_token={request_token}&perms={perms}"


def get_access_token(consumer_key: str, consumer_secret: str, request_token: str,
                      request_token_secret: str, verifier: str) -> dict:
    params = build_oauth_params(consumer_key, token=request_token)
    params['oauth_verifier'] = verifier
    body = signed_get(ACCESS_TOKEN_URL, params, consumer_key, consumer_secret, request_token, request_token_secret)
    return dict(urllib.parse.parse_qsl(body))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_flickr_oauth.py -v`
Expected: 4 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add flickr_oauth.py tests/test_flickr_oauth.py
git commit -m "feat: OAuth 1.0a signing and token exchange for Flickr API"
```

---

### Task 3: Flickr API client

**Files:**
- Create: `flickr_client.py`
- Test: `tests/test_flickr_client.py`

**Interfaces:**
- Consumes: `flickr_oauth.signed_get`, `flickr_oauth.build_oauth_params` (Task 2)
- Produces:
  - `class FlickrAPIError(Exception)`
  - `class FlickrClient.__init__(self, consumer_key, consumer_secret, access_token, access_token_secret)`
  - `FlickrClient.call(self, method: str, **params) -> dict`
  - `FlickrClient.get_photos_page(self, page: int, per_page: int = 100) -> list[dict]` — ogni elemento: `{"id": str, "title": str, "tags": list[str]}`
  - `FlickrClient.get_photo_original_bytes(self, photo_id: str) -> bytes`
  - `FlickrClient.get_photo_description(self, photo_id: str) -> str`
  - `FlickrClient.set_description(self, photo_id: str, description: str) -> None`
  - `FlickrClient.add_tag(self, photo_id: str, tag: str) -> None`
- Consumato da Task 7 (`scanner.py`).

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_flickr_client.py
import json
from unittest.mock import patch
import pytest
from flickr_client import FlickrClient, FlickrAPIError


def make_client():
    return FlickrClient(
        consumer_key='ck', consumer_secret='cs',
        access_token='at', access_token_secret='ats',
    )


@patch('flickr_client.flickr_oauth.signed_get')
def test_call_raises_on_fail_status(mock_signed_get):
    mock_signed_get.return_value = json.dumps({"stat": "fail", "code": 1, "message": "boom"})
    client = make_client()
    with pytest.raises(FlickrAPIError, match="boom"):
        client.call('flickr.test.echo')


@patch('flickr_client.flickr_oauth.signed_get')
def test_call_returns_parsed_json_on_success(mock_signed_get):
    mock_signed_get.return_value = json.dumps({"stat": "ok", "value": 42})
    client = make_client()
    result = client.call('flickr.test.echo')
    assert result == {"stat": "ok", "value": 42}


@patch('flickr_client.flickr_oauth.signed_get')
def test_get_photos_page_extracts_id_title_tags(mock_signed_get):
    mock_signed_get.return_value = json.dumps({
        "stat": "ok",
        "photos": {"photo": [
            {"id": "111", "title": "PXL_1.MP", "tags": "flickrmp:status=checked vacation"},
            {"id": "222", "title": "PXL_2.MP", "tags": ""},
        ]},
    })
    client = make_client()
    photos = client.get_photos_page(page=1)
    assert photos == [
        {"id": "111", "title": "PXL_1.MP", "tags": ["flickrmp:status=checked", "vacation"]},
        {"id": "222", "title": "PXL_2.MP", "tags": []},
    ]


@patch('flickr_client.flickr_oauth.signed_get')
def test_get_photo_original_bytes_downloads_from_static_url(mock_signed_get):
    def side_effect(url, params, *args, **kwargs):
        if 'getInfo' in params.get('method', ''):
            return json.dumps({
                "stat": "ok",
                "photo": {"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
            })
        raise AssertionError(f"unexpected signed_get call to {url}")

    mock_signed_get.side_effect = side_effect
    client = make_client()

    with patch('flickr_client.urllib.request.urlopen') as mock_urlopen:
        mock_urlopen.return_value.__enter__.return_value.read.return_value = b'JPEGBYTES'
        data = client.get_photo_original_bytes('999')

    assert data == b'JPEGBYTES'
    called_url = mock_urlopen.call_args[0][0]
    assert called_url == 'https://live.staticflickr.com/65535/999_abc123_o.jpg'


@patch('flickr_client.flickr_oauth.signed_get')
def test_get_photo_description_reads_from_getinfo(mock_signed_get):
    mock_signed_get.return_value = json.dumps({
        "stat": "ok",
        "photo": {"description": {"_content": "Una bella foto"}},
    })
    client = make_client()
    assert client.get_photo_description('999') == "Una bella foto"


@patch('flickr_client.flickr_oauth.signed_get')
def test_set_description_calls_setmeta_with_title_and_description(mock_signed_get):
    mock_signed_get.side_effect = [
        json.dumps({"stat": "ok", "photo": {"title": {"_content": "PXL_1.MP"}, "description": {"_content": ""}}}),
        json.dumps({"stat": "ok"}),
    ]
    client = make_client()
    client.set_description('999', 'nuova descrizione')
    second_call_params = mock_signed_get.call_args_list[1].args[1]
    assert second_call_params['method'] == 'flickr.photos.setMeta'
    assert second_call_params['description'] == 'nuova descrizione'
    assert second_call_params['title'] == 'PXL_1.MP'


@patch('flickr_client.flickr_oauth.signed_get')
def test_add_tag_calls_addtags(mock_signed_get):
    mock_signed_get.return_value = json.dumps({"stat": "ok"})
    client = make_client()
    client.add_tag('999', 'flickrmp:status=published')
    params = mock_signed_get.call_args.args[1]
    assert params['method'] == 'flickr.photos.addTags'
    assert params['photo_id'] == '999'
    assert params['tags'] == '"flickrmp:status=published"'
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_flickr_client.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'flickr_client'`

- [ ] **Step 3: Write the implementation**

```python
# flickr_client.py
import json
import urllib.request
import flickr_oauth

API_URL = 'https://api.flickr.com/services/rest'


class FlickrAPIError(Exception):
    pass


class FlickrClient:
    def __init__(self, consumer_key, consumer_secret, access_token, access_token_secret):
        self.consumer_key = consumer_key
        self.consumer_secret = consumer_secret
        self.access_token = access_token
        self.access_token_secret = access_token_secret

    def call(self, method: str, **params) -> dict:
        oauth_params = flickr_oauth.build_oauth_params(self.consumer_key, token=self.access_token)
        full_params = dict(oauth_params)
        full_params['method'] = method
        full_params['format'] = 'json'
        full_params['nojsoncallback'] = '1'
        full_params.update(params)

        body = flickr_oauth.signed_get(
            API_URL, full_params, self.consumer_key, self.consumer_secret,
            token=self.access_token, token_secret=self.access_token_secret,
        )
        result = json.loads(body)
        if result.get('stat') != 'ok':
            raise FlickrAPIError(result.get('message', 'unknown Flickr API error'))
        return result

    def get_photos_page(self, page: int, per_page: int = 100) -> list:
        result = self.call('flickr.people.getPhotos', user_id='me', extras='tags', page=page, per_page=per_page)
        photos = []
        for p in result['photos']['photo']:
            raw_tags = p.get('tags', '') or ''
            tags = raw_tags.split(' ') if raw_tags else []
            photos.append({'id': p['id'], 'title': p['title'], 'tags': tags})
        return photos

    def get_photo_original_bytes(self, photo_id: str) -> bytes:
        info = self.call('flickr.photos.getInfo', photo_id=photo_id)['photo']
        url = (
            f"https://live.staticflickr.com/{info['server']}/"
            f"{info['id']}_{info['originalsecret']}_o.{info['originalformat']}"
        )
        with urllib.request.urlopen(url) as response:
            return response.read()

    def get_photo_description(self, photo_id: str) -> str:
        info = self.call('flickr.photos.getInfo', photo_id=photo_id)['photo']
        return info['description']['_content']

    def set_description(self, photo_id: str, description: str) -> None:
        info = self.call('flickr.photos.getInfo', photo_id=photo_id)['photo']
        title = info['title']['_content']
        self.call('flickr.photos.setMeta', photo_id=photo_id, title=title, description=description)

    def add_tag(self, photo_id: str, tag: str) -> None:
        self.call('flickr.photos.addTags', photo_id=photo_id, tags=f'"{tag}"')
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_flickr_client.py -v`
Expected: 7 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add flickr_client.py tests/test_flickr_client.py
git commit -m "feat: Flickr API client for photo listing, original download, tags, description"
```

---

### Task 4: Site page generator

**Files:**
- Create: `site_generator.py`
- Create: `site/template.html`
- Test: `tests/test_site_generator.py`

**Interfaces:**
- Produces:
  - `render_photo_page(template: str, *, photo_id: str, title: str, image_url: str, video_url: str, photo_page_url: str) -> str`
  - `write_photo_page(site_dir, photo_id: str, html: str) -> "pathlib.Path"`
- Consumato da Task 7 (`scanner.py`).

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_site_generator.py
from pathlib import Path
from site_generator import render_photo_page, write_photo_page

TEMPLATE = """<!doctype html>
<title>{{TITLE}}</title>
<img src="{{IMAGE_URL}}">
<video src="{{VIDEO_URL}}" autoplay muted loop playsinline></video>
<a href="{{PHOTO_PAGE_URL}}">Vedi su Flickr</a>
"""


def test_render_photo_page_substitutes_all_placeholders():
    html = render_photo_page(
        TEMPLATE,
        photo_id="123",
        title="PXL_20260711.MP",
        image_url="https://live.staticflickr.com/x/123_o.jpg",
        video_url="https://videos.example.com/123.mp4",
        photo_page_url="https://www.flickr.com/photos/lorello/123/",
    )
    assert "{{" not in html
    assert "PXL_20260711.MP" in html
    assert "https://live.staticflickr.com/x/123_o.jpg" in html
    assert "https://videos.example.com/123.mp4" in html
    assert "https://www.flickr.com/photos/lorello/123/" in html


def test_write_photo_page_writes_to_site_p_photo_id_html(tmp_path):
    site_dir = tmp_path / "site"
    site_dir.mkdir()
    result_path = write_photo_page(site_dir, "123", "<html>ciao</html>")
    assert result_path == site_dir / "p" / "123.html"
    assert result_path.read_text() == "<html>ciao</html>"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_site_generator.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'site_generator'`

- [ ] **Step 3: Write the implementation**

```python
# site_generator.py
from pathlib import Path


def render_photo_page(template: str, *, photo_id: str, title: str, image_url: str,
                       video_url: str, photo_page_url: str) -> str:
    return (
        template
        .replace("{{TITLE}}", title)
        .replace("{{IMAGE_URL}}", image_url)
        .replace("{{VIDEO_URL}}", video_url)
        .replace("{{PHOTO_PAGE_URL}}", photo_page_url)
    )


def write_photo_page(site_dir: Path, photo_id: str, html: str) -> Path:
    pages_dir = Path(site_dir) / "p"
    pages_dir.mkdir(parents=True, exist_ok=True)
    output_path = pages_dir / f"{photo_id}.html"
    output_path.write_text(html)
    return output_path
```

Template ispirato alla Flickr photo page: sfondo scuro, foto/video centrati, titolo e link di ritorno.

```html
<!-- site/template.html -->
<!doctype html>
<html lang="it">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<style>
  :root { color-scheme: dark; }
  body {
    margin: 0;
    background: #131313;
    color: #eee;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex;
    flex-direction: column;
    align-items: center;
    min-height: 100vh;
  }
  header {
    width: 100%;
    padding: 16px 24px;
    box-sizing: border-box;
    text-align: left;
  }
  header a {
    color: #0063dc;
    text-decoration: none;
    font-weight: 600;
  }
  main {
    display: flex;
    flex: 1;
    align-items: center;
    justify-content: center;
    width: 100%;
    padding: 0 16px 32px;
    box-sizing: border-box;
  }
  .stage {
    position: relative;
    max-width: 100%;
  }
  .stage img, .stage video {
    max-width: 100%;
    max-height: 82vh;
    display: block;
    border-radius: 4px;
  }
  .stage video {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
    border-radius: 4px;
  }
  h1 {
    font-size: 1rem;
    font-weight: 400;
    color: #bbb;
    margin: 16px 0 0;
    text-align: center;
  }
</style>
</head>
<body>
  <header><a href="{{PHOTO_PAGE_URL}}">&larr; Vedi su Flickr</a></header>
  <main>
    <div class="stage">
      <img src="{{IMAGE_URL}}" alt="{{TITLE}}">
      <video src="{{VIDEO_URL}}" autoplay muted loop playsinline></video>
    </div>
  </main>
  <h1>{{TITLE}}</h1>
</body>
</html>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_site_generator.py -v`
Expected: 2 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add site_generator.py site/template.html tests/test_site_generator.py
git commit -m "feat: static viewer page generator with Flickr-like dark template"
```

---

### Task 5: Cloudflare R2 video upload

**Files:**
- Create: `r2_upload.py`
- Modify: `requirements.txt` (crea se non esiste)
- Test: `tests/test_r2_upload.py`

**Interfaces:**
- Produces:
  - `make_r2_client(account_id: str, access_key_id: str, secret_access_key: str)` — ritorna un client boto3 S3-compatibile
  - `upload_video(s3_client, bucket: str, public_base_url: str, photo_id: str, video_bytes: bytes) -> str` — ritorna l'URL pubblico
- Consumato da Task 7 (`scanner.py`).

- [ ] **Step 1: Write the failing tests**

```python
# tests/test_r2_upload.py
from unittest.mock import MagicMock
from r2_upload import upload_video, make_r2_client


def test_upload_video_puts_object_with_correct_key_and_content_type():
    mock_client = MagicMock()
    url = upload_video(mock_client, bucket="motion-photos", public_base_url="https://videos.example.com",
                        photo_id="12345", video_bytes=b'FAKEVIDEO')
    mock_client.put_object.assert_called_once_with(
        Bucket="motion-photos",
        Key="12345.mp4",
        Body=b'FAKEVIDEO',
        ContentType="video/mp4",
    )
    assert url == "https://videos.example.com/12345.mp4"


def test_make_r2_client_configures_correct_endpoint(monkeypatch):
    captured = {}

    class FakeBoto3:
        @staticmethod
        def client(service_name, **kwargs):
            captured['service_name'] = service_name
            captured.update(kwargs)
            return "fake-client"

    monkeypatch.setattr("r2_upload.boto3", FakeBoto3)
    client = make_r2_client(account_id="acct123", access_key_id="key", secret_access_key="secret")
    assert client == "fake-client"
    assert captured['service_name'] == 's3'
    assert captured['endpoint_url'] == 'https://acct123.r2.cloudflarestorage.com'
    assert captured['aws_access_key_id'] == 'key'
    assert captured['aws_secret_access_key'] == 'secret'
    assert captured['region_name'] == 'auto'
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_r2_upload.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'r2_upload'`

- [ ] **Step 3: Write the implementation**

```
# requirements.txt
boto3>=1.34
```

```python
# r2_upload.py
import boto3


def make_r2_client(account_id: str, access_key_id: str, secret_access_key: str):
    return boto3.client(
        's3',
        endpoint_url=f'https://{account_id}.r2.cloudflarestorage.com',
        aws_access_key_id=access_key_id,
        aws_secret_access_key=secret_access_key,
        region_name='auto',
    )


def upload_video(s3_client, bucket: str, public_base_url: str, photo_id: str, video_bytes: bytes) -> str:
    key = f"{photo_id}.mp4"
    s3_client.put_object(Bucket=bucket, Key=key, Body=video_bytes, ContentType="video/mp4")
    return f"{public_base_url}/{key}"
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && pip install -r requirements.txt && python3 -m pytest tests/test_r2_upload.py -v`
Expected: 2 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add r2_upload.py requirements.txt tests/test_r2_upload.py
git commit -m "feat: upload extracted motion videos to Cloudflare R2"
```

---

### Task 6: Git publish helper

**Files:**
- Create: `git_publish.py`
- Test: `tests/test_git_publish.py`

**Interfaces:**
- Produces: `publish(repo_root, message: str) -> bool` — esegue `git add -A`, poi `git commit -m message`; se non ci sono modifiche non fallisce (ritorna `False`); esegue `git push` solo se il commit è stato creato (ritorna `True`).
- Consumato da Task 7 (`scanner.py`).

- [ ] **Step 1: Write the failing test**

Il test usa una repo git reale in una directory temporanea con un remote "origin" locale (bare repo), cosi il push viene esercitato per davvero senza toccare la rete.

```python
# tests/test_git_publish.py
import subprocess
from pathlib import Path
from git_publish import publish


def _init_repo_with_local_remote(tmp_path: Path) -> Path:
    remote = tmp_path / "remote.git"
    subprocess.run(["git", "init", "--bare", str(remote)], check=True, capture_output=True)

    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init"], cwd=repo, check=True, capture_output=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], cwd=repo, check=True)
    subprocess.run(["git", "config", "user.name", "Test"], cwd=repo, check=True)
    subprocess.run(["git", "remote", "add", "origin", str(remote)], cwd=repo, check=True)
    (repo / "README.md").write_text("init")
    subprocess.run(["git", "add", "-A"], cwd=repo, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=repo, check=True, capture_output=True)
    subprocess.run(["git", "push", "-u", "origin", "master"], cwd=repo, check=True, capture_output=True)
    return repo


def test_publish_commits_and_pushes_when_there_are_changes(tmp_path):
    repo = _init_repo_with_local_remote(tmp_path)
    (repo / "site" / "p").mkdir(parents=True)
    (repo / "site" / "p" / "123.html").write_text("<html></html>")

    pushed = publish(repo, "publish motion photo 123")

    assert pushed is True
    log = subprocess.run(["git", "log", "-1", "--pretty=%s"], cwd=repo, check=True,
                          capture_output=True, text=True).stdout.strip()
    assert log == "publish motion photo 123"


def test_publish_returns_false_when_nothing_changed(tmp_path):
    repo = _init_repo_with_local_remote(tmp_path)

    pushed = publish(repo, "no-op")

    assert pushed is False
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_git_publish.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'git_publish'`

- [ ] **Step 3: Write the implementation**

```python
# git_publish.py
import subprocess
from pathlib import Path


def publish(repo_root: Path, message: str) -> bool:
    repo_root = Path(repo_root)
    subprocess.run(["git", "add", "-A"], cwd=repo_root, check=True, capture_output=True)

    result = subprocess.run(
        ["git", "commit", "-m", message], cwd=repo_root, capture_output=True, text=True,
    )
    if result.returncode != 0:
        if "nothing to commit" in result.stdout or "nothing to commit" in result.stderr:
            return False
        raise RuntimeError(f"git commit failed: {result.stdout}\n{result.stderr}")

    subprocess.run(["git", "push"], cwd=repo_root, check=True, capture_output=True)
    return True
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_git_publish.py -v`
Expected: 2 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add git_publish.py tests/test_git_publish.py
git commit -m "feat: idempotent git commit+push helper for publishing viewer pages"
```

---

### Task 7: Scanner CLI orchestrator

**Files:**
- Create: `config.py`
- Create: `scanner.py`
- Test: `tests/test_scanner.py`

**Interfaces:**
- Consumes: `motion_detect.detect_motion_photo` (Task 1), `flickr_client.FlickrClient` (Task 3), `site_generator.render_photo_page`/`write_photo_page` (Task 4), `r2_upload.upload_video` (Task 5), `git_publish.publish` (Task 6)
- Produces:
  - `Config` dataclass e `load_config() -> Config`
  - `process_photo(client, s3_client, config, photo: dict, dry_run: bool) -> str` — ritorna `"published"|"checked"|"lost"|"skipped"` (già taggata)
  - `run(client, s3_client, config, limit, dry_run) -> None`
  - `main() -> None` (entrypoint CLI con argparse: `--dry-run`, `--limit N`)

- [ ] **Step 1: Write config.py (nessun test dedicato: è I/O di configurazione, verificato indirettamente dai test di scanner.py con `Config` costruito a mano)**

```python
# config.py
import os
from dataclasses import dataclass
from pathlib import Path

REQUIRED_ENV_VARS = [
    "FLICKRGO_API_KEY",
    "FLICKRGO_API_SECRET",
    "FLICKR_OAUTH_TOKEN",
    "FLICKR_OAUTH_TOKEN_SECRET",
    "R2_ACCOUNT_ID",
    "R2_ACCESS_KEY_ID",
    "R2_SECRET_ACCESS_KEY",
    "R2_BUCKET",
    "R2_PUBLIC_BASE_URL",
]


@dataclass
class Config:
    flickr_api_key: str
    flickr_api_secret: str
    flickr_oauth_token: str
    flickr_oauth_token_secret: str
    r2_account_id: str
    r2_access_key_id: str
    r2_secret_access_key: str
    r2_bucket: str
    r2_public_base_url: str
    site_repo_root: Path


def load_config() -> Config:
    missing = [name for name in REQUIRED_ENV_VARS if not os.environ.get(name)]
    if missing:
        raise RuntimeError(
            "Variabili d'ambiente mancanti: " + ", ".join(missing) +
            ". Esegui bw-unlock o controlla Bitwarden 'Personal Secrets'."
        )
    return Config(
        flickr_api_key=os.environ["FLICKRGO_API_KEY"],
        flickr_api_secret=os.environ["FLICKRGO_API_SECRET"],
        flickr_oauth_token=os.environ["FLICKR_OAUTH_TOKEN"],
        flickr_oauth_token_secret=os.environ["FLICKR_OAUTH_TOKEN_SECRET"],
        r2_account_id=os.environ["R2_ACCOUNT_ID"],
        r2_access_key_id=os.environ["R2_ACCESS_KEY_ID"],
        r2_secret_access_key=os.environ["R2_SECRET_ACCESS_KEY"],
        r2_bucket=os.environ["R2_BUCKET"],
        r2_public_base_url=os.environ["R2_PUBLIC_BASE_URL"],
        site_repo_root=Path(__file__).resolve().parent,
    )
```

- [ ] **Step 2: Write the failing tests for scanner.py**

```python
# tests/test_scanner.py
from unittest.mock import MagicMock, patch
from pathlib import Path
from config import Config
from motion_detect import MotionPhotoResult
from scanner import process_photo


def make_config(tmp_path):
    return Config(
        flickr_api_key="k", flickr_api_secret="s",
        flickr_oauth_token="t", flickr_oauth_token_secret="ts",
        r2_account_id="acct", r2_access_key_id="rk", r2_secret_access_key="rs",
        r2_bucket="bucket", r2_public_base_url="https://videos.example.com",
        site_repo_root=tmp_path,
    )


def test_process_photo_skips_if_already_tagged():
    client = MagicMock()
    photo = {"id": "1", "title": "t", "tags": ["flickrmp:status=published"]}
    status = process_photo(client, MagicMock(), MagicMock(), photo, dry_run=False)
    assert status == "skipped"
    client.get_photo_original_bytes.assert_not_called()


@patch("scanner.detect_motion_photo")
def test_process_photo_checked_when_not_motion(mock_detect, tmp_path):
    mock_detect.return_value = MotionPhotoResult(is_motion=False, video_bytes=None, reason="checked")
    client = MagicMock()
    client.get_photo_original_bytes.return_value = b'JPEGDATA'
    photo = {"id": "2", "title": "t", "tags": []}

    status = process_photo(client, MagicMock(), make_config(tmp_path), photo, dry_run=False)

    assert status == "checked"
    client.add_tag.assert_called_once_with("2", "flickrmp:status=checked")
    client.set_description.assert_not_called()


@patch("scanner.detect_motion_photo")
def test_process_photo_lost_when_xmp_but_no_video(mock_detect, tmp_path):
    mock_detect.return_value = MotionPhotoResult(is_motion=False, video_bytes=None, reason="lost")
    client = MagicMock()
    client.get_photo_original_bytes.return_value = b'JPEGDATA'
    photo = {"id": "3", "title": "t", "tags": []}

    status = process_photo(client, MagicMock(), make_config(tmp_path), photo, dry_run=False)

    assert status == "lost"
    client.add_tag.assert_called_once_with("3", "flickrmp:status=lost")


@patch("scanner.publish")
@patch("scanner.upload_video")
@patch("scanner.detect_motion_photo")
def test_process_photo_published_full_flow(mock_detect, mock_upload_video, mock_publish, tmp_path):
    mock_detect.return_value = MotionPhotoResult(is_motion=True, video_bytes=b'VIDEO', reason="published")
    mock_upload_video.return_value = "https://videos.example.com/4.mp4"

    client = MagicMock()
    client.get_photo_original_bytes.return_value = b'JPEGDATA'
    client.get_photo_description.return_value = "Descrizione originale"
    photo = {"id": "4", "title": "PXL_4.MP", "tags": []}

    status = process_photo(client, MagicMock(), make_config(tmp_path), photo, dry_run=False)

    assert status == "published"
    mock_upload_video.assert_called_once()
    mock_publish.assert_called_once()
    client.set_description.assert_called_once()
    description_arg = client.set_description.call_args.args[1]
    assert "Descrizione originale" in description_arg
    assert tmp_path.name in description_arg or "http" in description_arg
    client.add_tag.assert_called_once_with("4", "flickrmp:status=published")
    assert (tmp_path / "site" / "p" / "4.html").exists()


@patch("scanner.detect_motion_photo")
def test_process_photo_idempotent_skips_description_if_link_already_present(mock_detect, tmp_path):
    mock_detect.return_value = MotionPhotoResult(is_motion=True, video_bytes=b'VIDEO', reason="published")
    client = MagicMock()
    client.get_photo_original_bytes.return_value = b'JPEGDATA'
    client.get_photo_description.return_value = "Guarda il video: https://motion.example.com/p/4.html"
    photo = {"id": "4", "title": "PXL_4.MP", "tags": []}

    config = make_config(tmp_path)
    config.r2_public_base_url = "https://videos.example.com"

    with patch("scanner.upload_video", return_value="https://videos.example.com/4.mp4"), \
         patch("scanner.publish"), \
         patch("scanner.VIEWER_BASE_URL", "https://motion.example.com"):
        status = process_photo(client, MagicMock(), config, photo, dry_run=False)

    assert status == "published"
    client.set_description.assert_not_called()


@patch("scanner.detect_motion_photo")
def test_process_photo_dry_run_makes_no_side_effects(mock_detect, tmp_path):
    mock_detect.return_value = MotionPhotoResult(is_motion=True, video_bytes=b'VIDEO', reason="published")
    client = MagicMock()
    client.get_photo_original_bytes.return_value = b'JPEGDATA'
    client.get_photo_description.return_value = ""
    photo = {"id": "5", "title": "PXL_5.MP", "tags": []}

    status = process_photo(client, MagicMock(), make_config(tmp_path), photo, dry_run=True)

    assert status == "published"
    client.set_description.assert_not_called()
    client.add_tag.assert_not_called()
    assert not (tmp_path / "site" / "p" / "5.html").exists()
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_scanner.py -v`
Expected: FAIL con `ModuleNotFoundError: No module named 'scanner'`

- [ ] **Step 4: Write the implementation**

```python
# scanner.py
import argparse
from motion_detect import detect_motion_photo
from site_generator import render_photo_page, write_photo_page
from r2_upload import make_r2_client, upload_video
from git_publish import publish
from flickr_client import FlickrClient
from config import load_config

VIEWER_BASE_URL = "https://motion.example.com"  # sostituire con il dominio GitHub Pages reale (Task 9)
STATUS_TAG_PREFIX = "flickrmp:status="


def _already_tagged(photo: dict) -> bool:
    return any(tag.startswith(STATUS_TAG_PREFIX) for tag in photo["tags"])


def process_photo(client, s3_client, config, photo: dict, dry_run: bool) -> str:
    if _already_tagged(photo):
        return "skipped"

    photo_id = photo["id"]
    jpeg_bytes = client.get_photo_original_bytes(photo_id)
    result = detect_motion_photo(jpeg_bytes)

    if not result.is_motion:
        if not dry_run:
            client.add_tag(photo_id, f"{STATUS_TAG_PREFIX}{result.reason}")
        return result.reason

    viewer_url = f"{VIEWER_BASE_URL}/p/{photo_id}.html"

    if dry_run:
        return "published"

    video_url = upload_video(s3_client, config.r2_bucket, config.r2_public_base_url, photo_id, result.video_bytes)

    template = (config.site_repo_root / "site" / "template.html").read_text()
    image_url = f"https://live.staticflickr.com/{photo_id}_o.jpg"  # placeholder risolto meglio in Task 9 con URL reale
    html = render_photo_page(
        template, photo_id=photo_id, title=photo["title"], image_url=image_url,
        video_url=video_url, photo_page_url=f"https://www.flickr.com/photos/lorello/{photo_id}/",
    )
    write_photo_page(config.site_repo_root / "site", photo_id, html)
    publish(config.site_repo_root, f"publish motion photo {photo_id}")

    current_description = client.get_photo_description(photo_id)
    if viewer_url not in current_description:
        new_description = f"{current_description}\n\nGuarda la motion photo: {viewer_url}".strip()
        client.set_description(photo_id, new_description)

    client.add_tag(photo_id, f"{STATUS_TAG_PREFIX}published")
    return "published"


def run(client, s3_client, config, limit, dry_run):
    processed = 0
    page = 1
    while limit is None or processed < limit:
        photos = client.get_photos_page(page=page)
        if not photos:
            break
        for photo in photos:
            if limit is not None and processed >= limit:
                break
            status = process_photo(client, s3_client, config, photo, dry_run)
            print(f"{photo['id']}\t{status}\t{photo['title']}")
            if status != "skipped":
                processed += 1
        page += 1


def main():
    parser = argparse.ArgumentParser(description="Scansiona le foto Flickr per Motion Photo Pixel")
    parser.add_argument("--dry-run", action="store_true", help="Rileva senza modificare Flickr/R2/git")
    parser.add_argument("--limit", type=int, default=None, help="Numero massimo di foto da processare")
    args = parser.parse_args()

    config = load_config()
    client = FlickrClient(
        config.flickr_api_key, config.flickr_api_secret,
        config.flickr_oauth_token, config.flickr_oauth_token_secret,
    )
    s3_client = make_r2_client(config.r2_account_id, config.r2_access_key_id, config.r2_secret_access_key)
    run(client, s3_client, config, args.limit, args.dry_run)


if __name__ == "__main__":
    main()
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && python3 -m pytest tests/test_scanner.py -v`
Expected: 6 passed

- [ ] **Step 6: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add config.py scanner.py tests/test_scanner.py
git commit -m "feat: scanner CLI orchestrating detection, upload, publish, tagging"
```

---

### Task 8: OAuth authorization one-off script + salvataggio credenziali

**Files:**
- Create: `authorize.py`

**Interfaces:**
- Consumes: `flickr_oauth.get_request_token`, `flickr_oauth.build_authorize_url`, `flickr_oauth.get_access_token` (Task 2)
- Produce: stampa a schermo `FLICKR_OAUTH_TOKEN` e `FLICKR_OAUTH_TOKEN_SECRET` da salvare manualmente in Bitwarden.

Questo script non necessita di test automatici: è un flusso interattivo one-off (già eseguito manualmente e validato con successo durante lo spike di design contro l'account reale `lorello`). L'implementazione replica esattamente la sequenza già provata.

- [ ] **Step 1: Scrivi lo script**

```python
# authorize.py
import os
import sys
import flickr_oauth


def main():
    consumer_key = os.environ.get("FLICKRGO_API_KEY")
    consumer_secret = os.environ.get("FLICKRGO_API_SECRET")
    if not consumer_key or not consumer_secret:
        print("Servono FLICKRGO_API_KEY e FLICKRGO_API_SECRET nell'ambiente (bw-unlock).", file=sys.stderr)
        sys.exit(1)

    request = flickr_oauth.get_request_token(consumer_key, consumer_secret)
    request_token = request["oauth_token"]
    request_token_secret = request["oauth_token_secret"]

    print("Apri questo URL, autorizza l'app, e incolla qui il verifier mostrato da Flickr:")
    print(flickr_oauth.build_authorize_url(request_token, perms="write"))
    verifier = input("Verifier: ").strip()

    access = flickr_oauth.get_access_token(
        consumer_key, consumer_secret, request_token, request_token_secret, verifier,
    )

    print("\nAutenticato come:", access["username"])
    print("\nSalva questi due valori in Bitwarden 'Personal Secrets' come campi custom:")
    print(f"  FLICKR_OAUTH_TOKEN = {access['oauth_token']}")
    print(f"  FLICKR_OAUTH_TOKEN_SECRET = {access['oauth_token_secret']}")
    print("\nPoi esegui bw-unlock in una nuova shell per caricarli.")


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Esegui manualmente e salva le credenziali**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
python3 authorize.py
```

Segui il flusso (già validato nello spike): apri l'URL, autorizza, incolla il verifier. Copia i due valori stampati in Bitwarden desktop/web, item "Personal Secrets", come nuovi campi custom `FLICKR_OAUTH_TOKEN ...` e `FLICKR_OAUTH_TOKEN_SECRET ...`. Esegui `bw-unlock` in una shell per ricaricare i secrets.

- [ ] **Step 3: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add authorize.py
git commit -m "feat: one-off OAuth authorization script"
```

---

### Task 9: Infrastruttura esterna (GitHub Pages + Cloudflare R2) e test end-to-end

**Files:**
- Modify: `scanner.py:11` (sostituire `VIEWER_BASE_URL` placeholder con il dominio reale di GitHub Pages)
- Nessun file di test: infrastruttura esterna, verificata con uno smoke test manuale reale seguito dal run end-to-end previsto dalla spec.

- [ ] **Step 1: Crea il repository GitHub per la pagina viewer**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
gh repo create lorello/flickr-motion-photos --public --source=. --remote=origin --push
```

- [ ] **Step 2: Abilita GitHub Pages sulla cartella `site/` del branch main**

```bash
gh api repos/lorello/flickr-motion-photos/pages -X POST -f "source[branch]=main" -f "source[path]=/site" 2>&1 || \
gh api repos/lorello/flickr-motion-photos/pages -X PUT -f "source[branch]=main" -f "source[path]=/site"
```

Verifica che la pagina sia raggiungibile (potrebbe richiedere 1-2 minuti dopo l'attivazione):

```bash
curl -s -o /dev/null -w "%{http_code}\n" https://lorello.github.io/flickr-motion-photos/
```

Expected: `200` (anche una index vuota va bene per ora, basta che Pages sia attivo)

- [ ] **Step 2b: Aggiorna `VIEWER_BASE_URL` in `scanner.py`**

```python
# scanner.py, riga 11
VIEWER_BASE_URL = "https://lorello.github.io/flickr-motion-photos"
```

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add scanner.py
git commit -m "chore: point VIEWER_BASE_URL to real GitHub Pages domain"
```

- [ ] **Step 3: Crea il bucket Cloudflare R2 e le credenziali**

Manuale via dashboard Cloudflare (dash.cloudflare.com → R2):
1. Crea bucket `flickr-motion-photos-videos`
2. Abilita "Public access" con dominio pubblico (o custom domain) — annota l'URL pubblico base (es. `https://pub-xxxx.r2.dev` o il tuo dominio custom)
3. Configura CORS sul bucket per permettere `GET` da qualsiasi origine (necessario per il tag `<video>` cross-origin):

```json
[
  {
    "AllowedOrigins": ["*"],
    "AllowedMethods": ["GET"],
    "AllowedHeaders": ["*"]
  }
]
```

4. Crea un R2 API Token (dashboard → R2 → Manage API Tokens) con permessi "Object Read & Write" limitati al bucket `flickr-motion-photos-videos`. Annota `Access Key ID` e `Secret Access Key`, e l'Account ID (visibile nell'URL della dashboard o nella pagina R2 overview).

- [ ] **Step 4: Salva le credenziali R2 in Bitwarden**

Aggiungi in Bitwarden "Personal Secrets" i campi custom:
- `R2_ACCOUNT_ID`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_BUCKET flickr-motion-photos-videos`
- `R2_PUBLIC_BASE_URL` (l'URL pubblico annotato al passo 3.2)

Esegui `bw-unlock` per caricarli nella shell corrente.

- [ ] **Step 5: Smoke test upload R2**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
python3 -c "
from r2_upload import make_r2_client, upload_video
import os
client = make_r2_client(os.environ['R2_ACCOUNT_ID'], os.environ['R2_ACCESS_KEY_ID'], os.environ['R2_SECRET_ACCESS_KEY'])
url = upload_video(client, os.environ['R2_BUCKET'], os.environ['R2_PUBLIC_BASE_URL'], 'smoketest', b'not a real video, just bytes')
print(url)
"
curl -s -o /dev/null -w "%{http_code}\n" "$(python3 -c "import os; print(os.environ['R2_PUBLIC_BASE_URL'] + '/smoketest.mp4')")"
```

Expected: ultimo comando stampa `200`

- [ ] **Step 6: Esegui `authorize.py` (Task 8) se non ancora fatto, poi test end-to-end reale**

Segui il flusso descritto dalla spec (`docs/superpowers/specs/2026-09-07-motion-photo-viewer-design.md`, sezione Testing):

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
bw-unlock   # carica tutti i secrets aggiornati
python3 scanner.py --dry-run --limit 5
```

Verifica nell'output che le foto motion note (es. `55391822566`) risultino `published` in dry-run senza errori.

```bash
python3 scanner.py --limit 5
```

Verifica manualmente:
- la pagina `https://lorello.github.io/flickr-motion-photos/p/55391822566.html` mostra foto+video in loop
- la descrizione della foto su Flickr contiene il link
- la foto ha il tag `flickrmp:status=published`

Rerun per verificare idempotenza:

```bash
python3 scanner.py --limit 5
```

Expected: tutte le foto del batch precedente risultano `skipped` (già taggate), nessun duplicato in descrizione.

- [ ] **Step 7: Commit finale di chiusura task**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add -A
git commit -m "chore: wire real GitHub Pages + R2 infra, validate end-to-end" --allow-empty
```

---

## Self-Review

**Copertura spec:**
- Detection XMP + payload MP4, offset `ftyp-4` → Task 1 ✅
- OAuth 1.0a locale, unico per CLI → Task 2, 8 ✅
- Download originale via `getInfo`, sempre autenticato → Task 3 ✅
- Machine tag `flickrmp:status=*` come stato, nessun file locale → Task 3 (tags), Task 7 (logica `_already_tagged`) ✅
- Pagina viewer stile dark Flickr-like → Task 4 ✅
- Upload video R2 → Task 5 ✅
- Publish GitHub Pages → Task 6 ✅
- Ordine idempotente (tag per ultimo, description-check, video overwrite-safe) → Task 7 ✅
- `--dry-run` / `--limit` → Task 7 ✅
- Secrets via Bitwarden → Task 8, 9 (istruzioni esplicite) ✅
- Testing end-to-end (dry-run → run reale → rerun idempotente) → Task 9 ✅
- Fase 2 (multi-utente, permessi Flickr) → esplicitamente fuori scope, non pianificata qui, coerente con la spec ✅

Nessun gap rilevato.

**Placeholder scan:** un solo placeholder intenzionale e dichiarato — `VIEWER_BASE_URL = "https://motion.example.com"` in Task 7, esplicitamente segnalato come da sostituire al Task 9 Step 2b (non è un "implementare dopo" generico: il valore reale e il comando esatto per sostituirlo sono nel piano). `image_url` in `process_photo` usa una formula placeholder (`.../{photo_id}_o.jpg`) perché l'URL reale richiede i campi `server`/`originalsecret`/`originalformat` già disponibili da `get_photo_original_bytes` ma non ritornati insieme ai bytes — **gap reale**, corretto qui sotto.

**Fix inline:** `FlickrClient.get_photo_original_bytes` ritorna solo i bytes, ma `scanner.process_photo` ha bisogno anche dell'URL immagine per la pagina viewer. Correzione: aggiungere un metodo `get_photo_original_url(self, photo_id: str) -> str` a `FlickrClient` (Task 3) e usarlo in `scanner.py` al posto del placeholder.

Aggiornamento Task 3 — aggiungere all'interfaccia e all'implementazione:

```python
# flickr_client.py — aggiungere dentro FlickrClient
def get_photo_original_url(self, photo_id: str) -> str:
    info = self.call('flickr.photos.getInfo', photo_id=photo_id)['photo']
    return (
        f"https://live.staticflickr.com/{info['server']}/"
        f"{info['id']}_{info['originalsecret']}_o.{info['originalformat']}"
    )
```

E il relativo test da aggiungere in `tests/test_flickr_client.py` (Task 3, Step 1):

```python
@patch('flickr_client.flickr_oauth.signed_get')
def test_get_photo_original_url_builds_static_flickr_url(mock_signed_get):
    mock_signed_get.return_value = json.dumps({
        "stat": "ok",
        "photo": {"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
    })
    client = make_client()
    url = client.get_photo_original_url('999')
    assert url == 'https://live.staticflickr.com/65535/999_abc123_o.jpg'
```

E in `scanner.py` (Task 7), sostituire la riga placeholder:

```python
# scanner.py — sostituire
image_url = f"https://live.staticflickr.com/{photo_id}_o.jpg"  # placeholder risolto meglio in Task 9 con URL reale
# con
image_url = client.get_photo_original_url(photo_id)
```

(Aggiunge una chiamata `getInfo` extra rispetto a `get_photo_original_bytes`, che internamente ne fa già una — duplicazione accettata per ora: ottimizzazione di caching lasciata fuori scope, coerente con YAGNI su un CLI a bassa frequenza d'uso.)

**Type consistency:** verificato — `MotionPhotoResult.reason` (Task 1) coincide esattamente con i valori usati in `STATUS_TAG_PREFIX` (Task 7) e nella spec. `Config` (Task 7) usa esattamente i nomi di campo consumati da `FlickrClient.__init__` e `make_r2_client`/`upload_video` (Task 3, 5). Nessuna discrepanza residua.
