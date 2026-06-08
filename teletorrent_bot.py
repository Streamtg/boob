#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v8.1 - CON TRANSMISSION CONFIGURADO
Descarga torrents reales, sube a Telegram, limpia automáticamente
"""

import asyncio
import hashlib
import logging
import os
import re
import signal
import sys
import time
import threading
from pathlib import Path
from typing import Optional, Dict, List

# ═══ IMPORTACIONES ═══════════════════════════════════════════════════════════
try:
    from telethon import TelegramClient, events
except ImportError:
    print("❌ ERROR: pip install telethon")
    sys.exit(1)

try:
    import requests
except ImportError:
    print("❌ ERROR: pip install requests")
    sys.exit(1)

try:
    import transmissionrpc
except ImportError:
    print("❌ ERROR: pip install transmission-rpc")
    sys.exit(1)

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
    """Configuración centralizada"""
    
    # Telegram
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    
    # Transmission
    TRANSMISSION_HOST = "127.0.0.1"
    TRANSMISSION_PORT = 9091
    TRANSMISSION_USERNAME = None
    TRANSMISSION_PASSWORD = None

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ CLIENTE TRANSMISSION ════════════════════════════════════════════════════
class TransmissionClient:
    """Cliente Transmission RPC"""

    def __init__(self):
        """Inicializa cliente"""
        self.torrents: Dict = {}
        self.lock = threading.Lock()
        
        try:
            log.info("🔌 Conectando a Transmission...")
            
            self.client = transmissionrpc.Client(
                host=Config.TRANSMISSION_HOST,
                port=Config.TRANSMISSION_PORT,
                username=Config.TRANSMISSION_USERNAME,
                password=Config.TRANSMISSION_PASSWORD,
            )
            
            # Verificar conexión
            session = self.client.get_session()
            log.info(f"✅ Transmission conectado")
            log.info(f"📁 Directorio: {session.download_dir}")
            
        except Exception as e:
            log.error(f"❌ Error conectando a Transmission: {e}")
            log.error(f"\nAsegúrate de que Transmission está corriendo:")
            log.error(f"  transmission-daemon -f &")
            sys.exit(1)

    def add_magnet(self, magnet_uri: str) -> Dict:
        """Agrega magnet"""
        try:
            log.info(f"📥 Agregando magnet...")
            torrent = self.client.add_torrent(magnet_uri)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                }
            
            log.info(f"✓ Magnet: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
            }
            
        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes, filename: str) -> Dict:
        """Agrega archivo .torrent"""
        try:
            log.info(f"📥 Agregando torrent...")
            
            # Guardar temporalmente
            temp_path = Path("/tmp") / f"temp_{int(time.time())}.torrent"
            temp_path.write_bytes(torrent_data)
            
            torrent = self.client.add_torrent(str(temp_path))
            
            # Eliminar temporal
            temp_path.unlink(missing_ok=True)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                }
            
            log.info(f"✓ Torrent: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
            }
            
        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def get_progress(self, torrent_id: int) -> Optional[Dict]:
        """Obtiene progreso"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            
            return {
                "name": torrent.name,
                "progress": torrent.progress,
                "downloaded": torrent.downloaded,
                "total": torrent.total_size,
                "speed": torrent.rateDownload,
                "eta": torrent.eta if torrent.eta != -1 else 0,
                "peers": torrent.num_seeds,
            }
            
        except:
            return None

    def get_files(self, torrent_id: int) -> List[str]:
        """Obtiene archivos"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            session = self.client.get_session()
            
            files = []
            download_dir = Path(session.download_dir)
            
            for file in torrent.files():
                file_path = download_dir / file['name']
                if file_path.exists() and file_path.stat().st_size > 0:
                    files.append(str(file_path))
            
            return files
            
        except:
            return []

    def remove_torrent(self, torrent_id: int, delete_data: bool = True):
        """Elimina torrent"""
        try:
            self.client.remove_torrent(torrent_id, delete_data=delete_data)
            
            with self.lock:
                if torrent_id in self.torrents:
                    del self.torrents[torrent_id]
            
            log.info(f"✓ Torrent eliminado")
            
        except Exception as e:
            log.warning(f"Error: {e}")

    def start_torrent(self, torrent_id: int):
        """Inicia descarga"""
        try:
            self.client.start_torrent(torrent_id)
            log.info(f"🚀 Descarga iniciada")
        except Exception as e:
            log.warning(f"Error: {e}")

    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño"""
        if n < 0:
            n = 0
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.1f} {u}"
            n /= 1024
        return f"{n:.1f} PB"

    def close(self):
        """Cierra"""
        try:
            self.client.close()
        except:
            pass

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TorrentBot:
    """Bot Telegram"""

    def __init__(self):
        self.transmission = TransmissionClient()
        
        self.active_tasks: Dict = {}
        self.status_msgs: Dict = {}
        
        self.client = TelegramClient(
            "bot_session",
            Config.API_ID,
            Config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✅ Bot inicializado")

    async def start(self):
        """Inicia bot"""
        try:
            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=Config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot: @{me.username}")
            
            try:
                ch = await self.client.get_entity(Config.CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except:
                pass
            
            self._handlers()
            
            log.info("🔍 Escuchando...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error: {e}")

    def _handlers(self):
        """Handlers"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_cmd(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v8.1*\n\n"
                "Con Transmission Real\n\n"
                "*Comandos:*\n"
                "/status - Progreso\n"
                "/cancel - Cancelar\n\n"
                "*Envía:*\n"
                "• Magnet link\n"
                "• Archivo .torrent\n"
                "• URL .torrent",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas*")
                return
            
            tid = self.active_tasks[cid]
            p = self.transmission.get_progress(tid)
            
            if not p:
                return
            
            bar = self._bar(int(p["progress"]))
            speed = TransmissionClient.format_size(int(p["speed"]))
            
            await event.reply(
                f"`{bar}` {p['progress']:.0f}%\n"
                f"{speed}/s\n"
                f"🌱 {p['peers']} peers"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                return
            
            tid = self.active_tasks[cid]
            self.transmission.remove_torrent(tid, delete_data=True)
            
            del self.active_tasks[cid]
            
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]

        @self.client.on(events.NewMessage)
        async def msg_handler(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            if text.startswith("magnet:"):
                await self._magnet_download(event, text)
                return
            
            if text.endswith(".torrent"):
                await self._url_torrent_download(event, text)
                return
            
            if event.message.document:
                await self._file_torrent(event)

    async def _magnet_download(self, event, magnet: str):
        """Descarga magnet"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Procesando magnet...*")
        self.status_msgs[cid] = sm
        
        try:
            info = self.transmission.add_magnet(magnet)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _url_torrent_download(self, event, url: str):
        """Descarga .torrent desde URL"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Descargando .torrent...*")
        self.status_msgs[cid] = sm
        
        try:
            r = requests.get(url, timeout=30)
            if r.status_code != 200:
                await sm.edit("*❌ Error*")
                return
            
            filename = url.split("/")[-1]
            info = self.transmission.add_torrent_file(r.content, filename)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _file_torrent(self, event):
        """Archivo .torrent"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        fname = event.message.document.file_name or "torrent.torrent"
        
        if not fname.lower().endswith(".torrent"):
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Procesando...*")
        self.status_msgs[cid] = sm
        
        try:
            data = await event.message.download_media(bytes)
            if not data:
                await sm.edit("*❌ Error*")
                return
            
            info = self.transmission.add_torrent_file(data, fname)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _monitor(self, cid: int):
        """Monitorea"""
        try:
            if cid not in self.active_tasks:
                return
            
            tid = self.active_tasks[cid]
            last_p = -1
            
            while cid in self.active_tasks:
                p = self.transmission.get_progress(tid)
                
                if not p:
                    await asyncio.sleep(1)
                    continue
                
                if int(p["progress"]) != last_p:
                    last_p = int(p["progress"])
                    
                    bar = self._bar(int(p["progress"]))
                    speed = TransmissionClient.format_size(int(p["speed"]))
                    
                    try:
                        if cid in self.status_msgs:
                            await self.status_msgs[cid].edit(
                                f"`{bar}` {p['progress']:.0f}%\n"
                                f"{speed}/s\n"
                                f"🌱 {p['peers']}"
                            )
                    except:
                        pass
                
                if p["progress"] >= 100:
                    break
                
                await asyncio.sleep(1)
            
            # Upload
            try:
                await self.status_msgs[cid].edit("*✅ Subiendo...*")
            except:
                pass
            
            files = self.transmission.get_files(tid)
            ok = 0
            
            for fpath in files[:5]:
                try:
                    fname = os.path.basename(fpath)
                    
                    await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=fpath,
                        caption=fname,
                        force_document=True
                    )
                    
                    try:
                        os.remove(fpath)
                    except:
                        pass
                    
                    ok += 1
                    await asyncio.sleep(0.5)
                except Exception as e:
                    log.error(f"Upload: {e}")
            
            try:
                await self.client.send_message(
                    cid,
                    f"*✅ {ok} archivo(s)*"
                )
            except:
                pass
            
            self.transmission.remove_torrent(tid, delete_data=True)
            
        except Exception as e:
            log.error(f"Monitor: {e}")
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                if cid in self.status_msgs:
                    del self.status_msgs[cid]

    @staticmethod
    def _bar(p: int) -> str:
        """Barra"""
        filled = min(int(p / 5), 20)
        return "█" * filled + "░" * (20 - filled)

    async def stop(self):
        """Detiene"""
        log.info("🛑 Deteniendo...")
        self.transmission.close()
        await self.client.disconnect()

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    try:
        await bot.start()
    except KeyboardInterrupt:
        pass
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 60)
    log.info("🚀 TeleTorrent Bot v8.1")
    log.info("=" * 60)
    
    bot = TorrentBot()
    
    def signal_handler(signum, frame):
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    asyncio.run(main(bot))
