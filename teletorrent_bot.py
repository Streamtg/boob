#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v12.1 - DEFINITIVA CON TORRENTS REALES
Descarga magnet links, torrents y URLs HTTP
Sube archivos a Telegram automáticamente
"""

import asyncio
import hashlib
import json
import logging
import os
import re
import shutil
import signal
import sys
import subprocess
import time
from pathlib import Path
from typing import Optional, Dict, List
from urllib.parse import quote

from telethon import TelegramClient, events
import requests

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    STORAGE_PATH = "./downloads"
    ARIA2_PORT = 6800

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ ARIA2 RPC CLIENT ════════════════════════════════════════════════════════
class Aria2RPC:
    """Cliente para aria2 RPC"""

    def __init__(self, port=Config.ARIA2_PORT):
        self.port = port
        self.url = f"http://localhost:{port}/rpc"
        self.request_id = 0
        self.ready = False
        self._wait_ready()

    def _wait_ready(self):
        """Espera a que aria2 esté listo"""
        for i in range(30):
            try:
                response = requests.get(
                    self.url,
                    timeout=2
                )
                self.ready = True
                log.info("✓ aria2 RPC conectado")
                return
            except:
                time.sleep(1)
        
        log.warning("⚠️ aria2 RPC no responde, intentando manualmente...")

    def _request(self, method: str, params: list = None) -> dict:
        """Realiza request JSON-RPC"""
        if not self.ready:
            self._wait_ready()
        
        try:
            self.request_id += 1
            
            payload = {
                "jsonrpc": "2.0",
                "id": self.request_id,
                "method": method,
                "params": params or []
            }
            
            response = requests.post(
                self.url,
                json=payload,
                timeout=10,
                headers={"Content-Type": "application/json"}
            )
            
            if response.status_code == 200:
                data = response.json()
                if "result" in data:
                    return data["result"]
                if "error" in data:
                    log.warning(f"aria2 error: {data['error']}")
                    return None
                return data
            else:
                log.warning(f"aria2 HTTP {response.status_code}")
                return None
            
        except requests.exceptions.ConnectionError:
            log.warning("aria2 no conecta - revisar si está corriendo")
            return None
        except Exception as e:
            log.warning(f"aria2 RPC: {e}")
            return None

    def add_uri(self, uri: str, options: dict = None) -> Optional[str]:
        """Agrega URI a aria2 - Retorna GID"""
        try:
            params = [[uri]]
            if options:
                params.append(options)
            
            result = self._request("aria2.addUri", params)
            
            if result and isinstance(result, str):
                log.info(f"✓ aria2 GID: {result}")
                return result
            
            return None
            
        except Exception as e:
            log.error(f"Error agregando URI: {e}")
            return None

    def get_status(self, gid: str) -> Optional[dict]:
        """Obtiene estado de descarga"""
        try:
            result = self._request("aria2.tellStatus", [
                gid,
                ["gid", "status", "totalLength", "completedLength", "downloadSpeed", "files"]
            ])
            
            return result if isinstance(result, dict) else None
            
        except Exception as e:
            log.warning(f"Error obteniendo status: {e}")
            return None

    def pause(self, gid: str):
        """Pausa descarga"""
        self._request("aria2.pause", [gid])

    def remove(self, gid: str):
        """Cancela descarga"""
        self._request("aria2.remove", [gid])

# ═══ GESTOR DE ARIA2 ═════════════════════════════════════════════════════════
class Aria2Manager:
    """Gestor de aria2c daemon"""

    @staticmethod
    def kill_existing():
        """Mata procesos aria2 existentes"""
        try:
            subprocess.run(["pkill", "-9", "aria2c"], timeout=5)
            time.sleep(1)
        except:
            pass

    @staticmethod
    def is_running() -> bool:
        """Verifica si aria2c está corriendo"""
        try:
            result = subprocess.run(
                ["pgrep", "-f", "aria2c"],
                capture_output=True,
                timeout=5
            )
            return result.returncode == 0
        except:
            return False

    @staticmethod
    def start(storage_path: str):
        """Inicia aria2c daemon correctamente"""
        Aria2Manager.kill_existing()

        log.info("🚀 Iniciando aria2c daemon...")

        # Crear directorio de storage
        Path(storage_path).mkdir(parents=True, exist_ok=True)

        # Crear archivo de sesión
        session_file = Path(storage_path) / "aria2_session.txt"
        session_file.touch()

        # Comando aria2c con configuración correcta
        cmd = [
            "aria2c",
            "--enable-rpc",
            "--rpc-listen-all=true",
            f"--rpc-listen-port={Config.ARIA2_PORT}",
            "--rpc-allow-origin-all=true",
            f"--dir={Path(storage_path).absolute()}",
            "--max-concurrent-downloads=3",
            "--max-connection-per-server=4",
            "--split=4",
            "--continue=true",
            "--file-allocation=falloc",
            "--disk-cache=32M",
            f"--save-session={session_file}",
            f"--load-cookies=/dev/null",
            "--daemon=true",
            "--log-level=info",
            f"--log={Path(storage_path) / 'aria2.log'}"
        ]

        try:
            # Ejecutar aria2c
            result = subprocess.Popen(
                cmd,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True
            )
            
            log.info(f"aria2c iniciado (PID: {result.pid})")
            time.sleep(3)

            # Verificar que esté corriendo
            if Aria2Manager.is_running():
                log.info("✓ aria2c verificado y corriendo")
                return True
            else:
                log.error("❌ aria2c no inició correctamente")
                return False

        except FileNotFoundError:
            log.error("❌ aria2c no encontrado. Instala: sudo apt-get install -y aria2")
            return False
        except Exception as e:
            log.error(f"❌ Error iniciando aria2c: {e}")
            return False

    @staticmethod
    def stop():
        """Detiene aria2c"""
        try:
            subprocess.run(["pkill", "-9", "aria2c"], timeout=5)
            log.info("✓ aria2c detenido")
        except:
            pass

# ═══ UTILIDADES ══════════════════════════════════════════════════════════════
class Utils:
    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño"""
        if n < 0 or n is None:
            n = 0
        n = float(n)
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.2f} {u}"
            n /= 1024
        return f"{n:.2f} PB"

    @staticmethod
    def progress_bar(p: float) -> str:
        """Barra de progreso"""
        if p is None:
            p = 0
        p = min(100, max(0, float(p)))
        filled = int(p / 5)
        return "█" * filled + "░" * (20 - filled)

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot Telegram con soporte completo de torrents"""

    def __init__(self):
        self.storage_path = Path(Config.STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)

        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()

        self.aria2 = None
        self.active_tasks: Dict = {}
        self.status_msgs: Dict = {}
        self.monitor_tasks: Dict = {}

        self.client = TelegramClient(
            "teletorrent_session",
            Config.API_ID,
            Config.API_HASH,
            connection_retries=5,
            timeout=30
        )

        log.info("✓ Bot inicializado")

    def _load_cache(self) -> dict:
        """Carga caché"""
        if self.cache_file.exists():
            try:
                return json.loads(self.cache_file.read_text())
            except:
                pass
        return {}

    def _save_cache(self):
        """Guarda caché"""
        try:
            self.cache_file.write_text(json.dumps(self.file_cache, indent=2))
        except:
            pass

    def _get_file_id(self, file_path: str) -> Optional[str]:
        """Obtiene file_id del caché"""
        if not os.path.exists(file_path):
            return None

        try:
            with open(file_path, "rb") as f:
                md5 = hashlib.md5(f.read()).hexdigest()

            for e in self.file_cache.values():
                if e.get("md5") == md5:
                    return e.get("file_id")
        except:
            pass

        return None

    def _cache_file_id(self, file_path: str, file_id: str):
        """Guarda file_id en caché"""
        if not os.path.exists(file_path):
            return

        try:
            with open(file_path, "rb") as f:
                md5 = hashlib.md5(f.read()).hexdigest()

            self.file_cache[os.path.basename(file_path)] = {
                "md5": md5,
                "file_id": file_id,
                "timestamp": time.time()
            }
            self._save_cache()
        except:
            pass

    async def start(self):
        """Inicia el bot"""
        try:
            # Iniciar aria2
            if not Aria2Manager.start(str(self.storage_path)):
                log.error("No se pudo iniciar aria2")
                sys.exit(1)

            # Inicializar RPC
            self.aria2 = Aria2RPC()

            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=Config.BOT_TOKEN)

            me = await self.client.get_me()
            log.info(f"✓ Bot: @{me.username} (ID: {me.id})")

            try:
                ch = await self.client.get_entity(Config.CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except Exception as e:
                log.warning(f"⚠️  Canal: {e}")

            self._register_handlers()

            log.info("🔍 Bot esperando comandos...")
            await self.client.run_until_disconnected()

        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def _register_handlers(self):
        """Registra handlers"""

        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_h(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v12.1*\n\n"
                "Descarga torrents y sube a Telegram\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Ayuda\n"
                "🔹 `/status` - Progreso\n"
                "🔹 `/cancel` - Cancelar\n\n"
                "*💾 Formatos soportados:*\n"
                "• Magnet links\n"
                "• URLs HTTP/HTTPS\n"
                "• Archivos .torrent",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_h(event):
            cid = event.chat_id

            if cid not in self.active_tasks:
                await event.reply("*⏸ Sin descargas*", parse_mode="markdown")
                return

            t = self.active_tasks[cid]
            p = self.aria2.get_status(t["gid"])

            if not p:
                await event.reply("*❌ Descarga no disponible*", parse_mode="markdown")
                return

            total = int(p.get("totalLength", 0))
            completed = int(p.get("completedLength", 0))
            speed = int(p.get("downloadSpeed", 0))

            progress = (completed / total * 100) if total > 0 else 0
            bar = Utils.progress_bar(progress)

            await event.reply(
                f"*⬇️ {t['name'][:30]}*\n\n"
                f"`{bar}` `{progress:.1f}%`\n"
                f"📊 {Utils.format_size(speed)}/s\n"
                f"📥 {Utils.format_size(completed)}/{Utils.format_size(total)}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_h(event):
            cid = event.chat_id

            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar*", parse_mode="markdown")
                return

            t = self.active_tasks[cid]
            self.aria2.remove(t["gid"])
            del self.active_tasks[cid]

            if cid in self.monitor_tasks:
                self.monitor_tasks[cid].cancel()
                del self.monitor_tasks[cid]

            if cid in self.status_messages:
                try:
                    await self.status_messages[cid].delete()
                except:
                    pass
                del self.status_messages[cid]

            await event.reply("*✓ Cancelado*", parse_mode="markdown")

        @self.client.on(events.NewMessage)
        async def msg_h(event):
            text = (event.message.text or "").strip()

            if text.startswith("/"):
                return

            if text.startswith("magnet:?xt="):
                await self._dl_magnet(event, text)
            elif text.startswith("http"):
                await self._dl_url(event, text)
            elif event.message.document:
                await self._dl_file(event)

    async def _dl_magnet(self, event, magnet: str):
        """Descarga magnet"""
        cid = event.chat_id

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa. /cancel*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Procesando magnet...*", parse_mode="markdown")
        self.status_messages[cid] = sm

        try:
            magnet = magnet.strip()
            if "&" in magnet:
                magnet = magnet[:magnet.index("&")]

            if not magnet.startswith("magnet:?xt="):
                await sm.edit("*❌ Magnet inválido*"); return

            await sm.edit("*📥 Agregando a aria2...*")
            gid = self.aria2.add_uri(magnet, {"dir": str(self.storage_path)})

            if not gid:
                await sm.edit("*❌ No se pudo agregar el magnet*"); return

            dn = re.search(r"dn=([^&]+)", magnet)
            name = dn.group(1) if dn else "Descarga"

            self.active_tasks[cid] = {"gid": gid, "name": name}

            await sm.edit(f"*✅ Descargando:* `{name[:40]}`\n\n`{self._pbar(0)}` 0%")
            self.monitor_tasks[cid] = asyncio.create_task(self._mon(cid, gid))

        except Exception as ex:
            log.error(f"Error: {ex}")
            await sm.edit(f"*❌ Error:* `{str(ex)[:80]}`")

    async def _dl_url(self, event, url: str):
        """Descarga URL HTTP"""
        cid = event.chat_id

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa. /cancel*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Iniciando descarga...*", parse_mode="markdown")
        self.status_messages[cid] = sm

        try:
            fn = url.split("/")[-1].split("?")[0] or "descarga"
            await sm.edit("*📥 Agregando a aria2...*")
            gid = self.aria2.add_uri(url, {"dir": str(self.storage_path), "out": fn})

            if not gid:
                await sm.edit("*❌ No se pudo agregar la URL*"); return

            self.active_tasks[cid] = {"gid": gid, "name": fn}

            await sm.edit(f"*✅ Descargando:* `{fn[:40]}`\n\n`{self._pbar(0)}` 0%")
            self.monitor_tasks[cid] = asyncio.create_task(self._mon(cid, gid))

        except Exception as ex:
            log.error(f"Error: {ex}")
            await sm.edit(f"*❌ Error:* `{str(ex)[:80]}`")

    async def _dl_file(self, event):
        """Descarga archivo adjunto"""
        if not event.message.document:
            return

        cid = event.chat_id
        doc = event.message.document
        fn = doc.file_name or "archivo"

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa. /cancel*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Descargando archivo...*", parse_mode="markdown")
        self.status_messages[cid] = sm

        try:
            fp = self.storage_path / fn
            await event.message.download_media(str(fp))

            if os.path.getsize(fp) == 0:
                await sm.edit("*❌ Archivo vacío*"); return

            await sm.edit("*✅ Descargado! Subiendo...*")
            await self._upload(cid, [str(fp)])

        except Exception as ex:
            log.error(f"Error: {ex}")
            await sm.edit(f"*❌ Error:* `{str(ex)[:80]}`")
        finally:
            if cid in self.status_messages:
                try: await self.status_messages[cid].delete()
                except: pass
                if cid in self.status_messages:
                    del self.status_messages[cid]

    async def _mon(self, cid, gid):
        """Monitorea descarga"""
        try:
            last_p = -1
            while cid in self.active_tasks:
                status = self.aria2.get_status(gid)
                if not status: break

                total = int(status.get("totalLength", 0))
                completed = int(status.get("completedLength", 0))
                speed = int(status.get("downloadSpeed", 0))
                st = status.get("status", "")

                progress = (completed / total * 100) if total > 0 else 0

                if int(progress) != last_p:
                    last_p = int(progress)
                    await self._upd_progress(cid, progress, speed, completed, total)

                if st == "complete":
                    break

                await asyncio.sleep(UPDATE_INTERVAL)

            # Upload
            if cid in self.active_tasks:
                await self._upd_msg(cid, "*⏳ Descarga completa! Subiendo...*")
                files = self._get_files(gid)
                if files:
                    await self._upload(cid, files)
                else:
                    await self._upd_msg(cid, "*⚠️ Sin archivos para subir*")

        except asyncio.CancelledError:
            log.info(f"Monitor cancelado: {cid}")
        except Exception as ex:
            log.error(f"Error en monitor: {ex}")
            await self._upd_msg(cid, f"*❌ Error:* `{str(ex)[:80]}`")
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            if cid in self.monitor_tasks:
                del self.monitor_tasks[cid]
            if cid in self.status_messages:
                try: await self.status_messages[cid].delete()
                except: pass
                if cid in self.status_messages:
                    del self.status_messages[cid]

    async def _upd_progress(self, cid, progress, speed, completed, total):
        """Actualiza progreso"""
        if cid not in self.status_messages: return
        bar = self._pbar(int(progress))
        try:
            await self.status_messages[cid].edit(
                f"*⬇️ Descargando*\n\n"
                f"`{bar}` `{progress:.1f}%`\n"
                f"📊 {Utils.format_size(speed)}/s\n"
                f"📥 {Utils.format_size(completed)}/{Utils.format_size(total)}"
            )
        except: pass

    async def _upd_msg(self, cid, text):
        """Actualiza mensaje"""
        if cid in self.status_messages:
            try: await self.status_messages[cid].edit(text)
            except: pass

    def _get_files(self, gid) -> list:
        """Obtiene archivos completados"""
        files = []
        for item in self.storage_path.rglob("*"):
            if item.is_file() and item.stat().st_size > 0:
                if not item.name.endswith((".torrent", ".conf", ".txt")):
                    files.append(str(item))
        return files[:10]

    @staticmethod
    def _pbar(p: int) -> str:
        """Barra de progreso"""
        p = min(100, max(0, p))
        filled = int(p / 5)
        return "█" * filled + "░" * (20 - filled)

    async def _upload(self, cid, files):
        """Sube archivos al canal"""
        uploaded, failed = 0, 0
        for fp in files:
            if not os.path.exists(fp): continue
            fs = os.path.getsize(fp)
            if fs == 0: continue
            fn = os.path.basename(fp)
            log.info(f"📤 Subiendo: {fn}")
            try:
                await self._upd_msg(cid, f"*📤 Subiendo:* `{fn[:40]}`")
                await self.client.send_file(CHANNEL_ID, file=fp, caption=fn, force_document=True)
                try: os.remove(fp)
                except: pass
                uploaded += 1
                log.info(f"✅ {fn}")
            except Exception as e:
                log.error(f"❌ {fn}: {e}")
                failed += 1
            await asyncio.sleep(1)
        
        msg = f"*✅ ¡Completado!*\n📦 {uploaded} enviado(s)"
        if failed: msg = f"*⚠️ Parcial:* ✅{uploaded} | ❌{failed}"
        await self.client.send_message(cid, msg, parse_mode="markdown")

    async def stop(self):
        """Detiene el bot"""
        log.info("🛑 Deteniendo bot...")
        self.torrent.close()
        await self.client.disconnect()

# ═══ UTILIDADES ══════════════════════════════════════════════════════════════
class Utils:
    @staticmethod
    def format_size(n: int) -> str:
        if n < 0 or n is None: n = 0
        n = float(n)
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024: return f"{n:.2f} {u}"
            n /= 1024
        return f"{n:.2f} PB"

async def main():
    """Punto de entrada"""
    bot = TeleTorrentBot()
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    finally:
        await bot.stop()
        cleanup_temp()

if __name__ == "__main__":
    asyncio.run(main())
