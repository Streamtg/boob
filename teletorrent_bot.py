#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v12.0 - DEFINITIVA CON TORRENTS REALES
Usa aria2c para torrents y descargas HTTP
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
import xml.etree.ElementTree as ET
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
        self.gid_map = {}

    def _request(self, method: str, params: list = None) -> dict:
        """Realiza request JSON-RPC"""
        try:
            payload = {
                "jsonrpc": "2.0",
                "id": int(time.time() * 1000),
                "method": method,
                "params": params or []
            }
            
            response = requests.post(
                self.url,
                json=payload,
                timeout=10
            )
            
            data = response.json()
            return data.get("result", {})
            
        except Exception as e:
            log.warning(f"Aria2 RPC error: {e}")
            return {}

    def add_uri(self, uri: str, options: dict = None) -> str:
        """Agrega URI a aria2"""
        params = [
            [uri],
            options or {}
        ]
        result = self._request("aria2.addUri", params)
        return result

    def get_status(self, gid: str) -> dict:
        """Obtiene estado de descarga"""
        result = self._request("aria2.tellStatus", [gid, [
            "gid", "status", "totalLength", "completedLength",
            "downloadSpeed", "files"
        ]])
        return result

    def pause(self, gid: str):
        """Pausa descarga"""
        self._request("aria2.pause", [gid])

    def remove(self, gid: str):
        """Cancela descarga"""
        self._request("aria2.remove", [gid])

    def get_active(self) -> list:
        """Obtiene descargas activas"""
        return self._request("aria2.tellActive", [["gid", "status", "files"]])

# ═══ GESTOR DE ARIA2 ═════════════════════════════════════════════════════════
class Aria2Manager:
    """Gestor de aria2c daemon"""

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
    def start():
        """Inicia aria2c daemon"""
        if Aria2Manager.is_running():
            log.info("✓ aria2c ya está corriendo")
            return

        log.info("🚀 Iniciando aria2c...")

        # Crear config
        config = f"""
enable-rpc=true
rpc-listen-port={Config.ARIA2_PORT}
rpc-listen-all=true
dir={Path(Config.STORAGE_PATH).absolute()}
max-concurrent-downloads=3
max-connection-per-server=4
split=4
continue=true
"""

        config_path = Path(Config.STORAGE_PATH) / "aria2.conf"
        config_path.write_text(config)

        try:
            subprocess.Popen(
                ["aria2c", "--conf-path", str(config_path)],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True
            )
            time.sleep(2)

            if Aria2Manager.is_running():
                log.info("✓ aria2c iniciado correctamente")
            else:
                log.error("❌ aria2c no inició")

        except FileNotFoundError:
            log.error("❌ aria2c no instalado. Instala: sudo apt-get install -y aria2")

    @staticmethod
    def stop():
        """Detiene aria2c"""
        try:
            subprocess.run(["pkill", "-f", "aria2c"], timeout=5)
            log.info("✓ aria2c detenido")
        except:
            pass

# ═══ UTILIDADES ══════════════════════════════════════════════════════════════
class Utils:
    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño"""
        if n < 0:
            n = 0
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.2f} {u}"
            n /= 1024
        return f"{n:.2f} PB"

    @staticmethod
    def progress_bar(p: float) -> str:
        """Barra de progreso"""
        p = min(100, max(0, p))
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

        self.aria2 = Aria2RPC()
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

        log.info("✅ Bot inicializado")

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
            Aria2Manager.start()

            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=Config.BOT_TOKEN)

            me = await self.client.get_me()
            log.info(f"✅ Bot: @{me.username} (ID: {me.id})")

            try:
                ch = await self.client.get_entity(Config.CHANNEL_ID)
                log.info(f"✅ Canal: {ch.title}")
            except:
                log.warning("⚠️  Canal no accesible")

            self._register_handlers()

            log.info("🔍 Escuchando mensajes...")
            await self.client.run_until_disconnected()

        except Exception as e:
            log.error(f"Error: {e}")

    def _register_handlers(self):
        """Registra handlers"""

        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_cmd(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v12.0*\n\n"
                "Descarga torrents y archivos\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar descarga\n\n"
                "*💾 Tipos de descarga:*\n"
                "• Magnet links: `magnet:?xt=...`\n"
                "• URLs HTTP/HTTPS\n"
                "• Archivos .torrent\n"
                "• Archivos adjuntos",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_cmd(event):
            cid = event.chat_id

            if cid not in self.active_tasks:
                await event.reply("*Sin descargas activas*", parse_mode="markdown")
                return

            task = self.active_tasks[cid]
            status = self.aria2.get_status(task["gid"])

            if not status:
                await event.reply("*Descarga no disponible*", parse_mode="markdown")
                return

            total = int(status.get("totalLength", 0))
            completed = int(status.get("completedLength", 0))
            speed = int(status.get("downloadSpeed", 0))

            progress = (completed / total * 100) if total > 0 else 0

            bar = Utils.progress_bar(progress)

            await event.reply(
                f"*⬇️ Descargando:* `{task['name'][:30]}`\n\n"
                f"`{bar}` `{progress:.1f}%`\n"
                f"📊 {Utils.format_size(speed)}/s\n"
                f"📥 {Utils.format_size(completed)} / {Utils.format_size(total)}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id

            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar*", parse_mode="markdown")
                return

            task = self.active_tasks[cid]
            self.aria2.remove(task["gid"])

            if cid in self.monitor_tasks:
                self.monitor_tasks[cid].cancel()
                del self.monitor_tasks[cid]

            del self.active_tasks[cid]

            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]

            await event.reply("*Cancelado ✓*", parse_mode="markdown")

        @self.client.on(events.NewMessage)
        async def msg_handler(event):
            text = (event.message.text or "").strip()

            if text.startswith("/"):
                return

            if text.startswith("magnet:?xt="):
                await self._download_magnet(event, text)
                return

            if text.startswith(("http://", "https://", "ftp://")):
                await self._download_url(event, text)
                return

            if event.message.document:
                await self._download_file(event)

    async def _download_magnet(self, event, magnet: str):
        """Descarga magnet"""
        cid = event.chat_id

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Procesando magnet...*", parse_mode="markdown")
        self.status_msgs[cid] = sm

        try:
            magnet = magnet.strip()
            if "&" in magnet:
                magnet = magnet[:magnet.index("&")]

            if not magnet.startswith("magnet:?xt="):
                await sm.edit("*❌ Magnet inválido*")
                return

            # Extraer nombre
            dn_match = re.search(r"dn=([^&]+)", magnet)
            name = dn_match.group(1) if dn_match else "descarga"

            await sm.edit("*📥 Agregando a aria2...*")

            gid = self.aria2.add_uri(magnet, {
                "dir": str(self.storage_path),
                "split": "4",
                "max-connection-per-server": "4",
            })

            if not gid:
                await sm.edit("*❌ Error agregando magnet*")
                return

            self.active_tasks[cid] = {
                "gid": gid,
                "name": name,
                "type": "magnet"
            }

            await sm.edit(
                f"*✅ Agregado:* `{name[:40]}`\n\n"
                f"`{'░' * 20}` 0%"
            )

            self.monitor_tasks[cid] = asyncio.create_task(
                self._monitor(cid, gid)
            )

        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:80]}`", parse_mode="markdown")

    async def _download_url(self, event, url: str):
        """Descarga URL"""
        cid = event.chat_id

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Procesando URL...*", parse_mode="markdown")
        self.status_msgs[cid] = sm

        try:
            filename = url.split("/")[-1].split("?")[0] or "descarga"

            await sm.edit("*📥 Agregando a aria2...*")

            gid = self.aria2.add_uri(url, {
                "dir": str(self.storage_path),
                "out": filename,
            })

            if not gid:
                await sm.edit("*❌ Error agregando URL*")
                return

            self.active_tasks[cid] = {
                "gid": gid,
                "name": filename,
                "type": "http"
            }

            await sm.edit(
                f"*✅ Agregado:* `{filename[:40]}`\n\n"
                f"`{'░' * 20}` 0%"
            )

            self.monitor_tasks[cid] = asyncio.create_task(
                self._monitor(cid, gid)
            )

        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:80]}`", parse_mode="markdown")

    async def _download_file(self, event):
        """Descarga archivo adjunto"""
        if not event.message.document:
            return

        cid = event.chat_id
        doc = event.message.document
        filename = doc.file_name or "archivo"

        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return

        sm = await event.reply("*⏳ Descargando...*", parse_mode="markdown")
        self.status_msgs[cid] = sm

        try:
            file_path = self.storage_path / filename

            await event.message.download_media(str(file_path))

            await sm.edit("*✅ Descargado! Subiendo...*", parse_mode="markdown")
            await self._upload_file(cid, file_path)

        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:80]}`", parse_mode="markdown")
        finally:
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                if cid in self.status_msgs:
                    del self.status_msgs[cid]

    async def _monitor(self, cid, gid: str):
        """Monitorea descarga"""
        try:
            last_p = -1

            while cid in self.active_tasks:
                status = self.aria2.get_status(gid)

                if not status:
                    await asyncio.sleep(2)
                    continue

                total = int(status.get("totalLength", 0))
                completed = int(status.get("completedLength", 0))
                speed = int(status.get("downloadSpeed", 0))
                state = status.get("status", "")

                progress = (completed / total * 100) if total > 0 else 0

                if int(progress) != last_p:
                    last_p = int(progress)

                    if cid in self.status_msgs:
                        bar = Utils.progress_bar(progress)
                        task_name = self.active_tasks.get(cid, {}).get("name", "descarga")

                        try:
                            await self.status_msgs[cid].edit(
                                f"*⬇️ Descargando:* `{task_name[:30]}`\n\n"
                                f"`{bar}` `{progress:.1f}%`\n"
                                f"📊 {Utils.format_size(speed)}/s\n"
                                f"📥 {Utils.format_size(completed)} / {Utils.format_size(total)}",
                                parse_mode="markdown"
                            )
                        except:
                            pass

                if state == "complete":
                    break

                await asyncio.sleep(2)

            # Subida
            if cid in self.active_tasks:
                await self._upd_msg(cid, "*✅ Descargado! Subiendo...*")
                files = self.torrent.get_completed_files(gid) if hasattr(self, 'torrent') else []

                # Buscar archivos descargados
                files = []
                for item in self.storage_path.rglob("*"):
                    if item.is_file() and item.stat().st_size > 0:
                        if not item.name.endswith((".torrent", ".conf")):
                            files.append(str(item))

                if files:
                    await self._upload(cid, files)
                else:
                    await self._upd_msg(cid, "*⚠️ Sin archivos para subir*")

        except asyncio.CancelledError:
            log.info(f"Monitor cancelado para {cid}")
        except Exception as e:
            log.error(f"Error en monitor: {e}")
            if cid in self.status_msgs:
                try:
                    await self._upd_msg(cid, f"*❌ Error:* `{str(e)[:80]}`")
                except:
                    pass
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            if cid in self.monitor_tasks:
                del self.monitor_tasks[cid]
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                if cid in self.status_msgs:
                    del self.status_msgs[cid]

    async def _upd_msg(self, cid, text):
        """Actualiza mensaje"""
        if cid in self.status_msgs:
            try:
                await self.status_msgs[cid].edit(text)
            except:
                pass

    async def _upload_file(self, cid, file_path: Path):
        """Sube archivo individual"""
        try:
            if not file_path.exists():
                await self.client.send_message(cid, "*❌ Archivo no encontrado*", parse_mode="markdown")
                return

            filename = file_path.name
            file_size = file_path.stat().st_size

            log.info(f"📤 Subiendo: {filename} ({Utils.format_size(file_size)})")

            response = await self.client.send_file(
                Config.CHANNEL_ID,
                file=str(file_path),
                caption=filename,
                force_document=True
            )

            if response and hasattr(response, "media"):
                if hasattr(response.media, "document"):
                    self._cache_file_id(str(file_path), str(response.media.document.id))

            try:
                file_path.unlink()
            except:
                pass

            await self.client.send_message(
                cid,
                f"*✅ Subido:* `{filename}`",
                parse_mode="markdown"
            )

        except Exception as e:
            log.error(f"Error subiendo: {e}")
            await self.client.send_message(cid, f"*❌ Error subiendo:* `{str(e)[:80]}`", parse_mode="markdown")

    async def _upload(self, cid, files):
        """Sube múltiples archivos"""
        uploaded = 0
        failed = 0

        for file_path in files[:5]:
            if not os.path.exists(file_path):
                continue

            file_size = os.path.getsize(file_path)

            if file_size == 0:
                continue

            filename = os.path.basename(file_path)

            try:
                await self._upd_msg(cid, f"*📤 Subiendo:* `{filename[:30]}`")

                response = await self.client.send_file(
                    Config.CHANNEL_ID,
                    file=file_path,
                    caption=filename,
                    force_document=True
                )

                if response and hasattr(response, "media"):
                    if hasattr(response.media, "document"):
                        self._cache_file_id(file_path, str(response.media.document.id))

                try:
                    os.remove(file_path)
                except:
                    pass

                uploaded += 1
                log.info(f"✅ {filename}")

            except Exception as e:
                log.error(f"Error: {e}")
                failed += 1

            await asyncio.sleep(1)

        if uploaded > 0:
            msg = f"*✅ Completado!* {uploaded} archivo(s) enviado(s)"
        else:
            msg = "*❌ Error en la subida*"

        if failed > 0:
            msg = f"*⚠️ Parcial:* {uploaded} OK, {failed} fallaron"

        try:
            await self.client.send_message(cid, msg, parse_mode="markdown")
        except:
            pass

    async def stop(self):
        """Detiene el bot"""
        log.info("🛑 Deteniendo...")
        Aria2Manager.stop()
        await self.client.disconnect()
        log.info("✓ Bot detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 70)
    log.info("🚀 TeleTorrent Bot v12.0 - INICIANDO")
    log.info("=" * 70)

    bot = TeleTorrentBot()

    def signal_handler(signum, frame):
        log.info("Señal recibida, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    try:
        asyncio.run(main(bot))
    except Exception as e:
        log.error(f"Error fatal: {e}")
        sys.exit(1)
