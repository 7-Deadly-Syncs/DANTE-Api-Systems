# Latar Belakang dan Konteks
Transaksi QRIS dan transaksi real-time (misalnya pembayaran, transfer, inquiry saldo) menuntut respon cepat agar pengguna tidak gagal transaksi atau mengulang pembayaran.  

Pada kenyataannya, latency tinggi sering muncul karena:
- Integrasi ke sistem lama (legacy)
- Beban puncak
- Query database yang tidak optimal
- Kondisi jaringan yang tidak merata, terutama di wilayah dengan kualitas 4G/5G yang rendah  

Latency yang tidak stabil menyebabkan:
- Penurunan pengalaman pengguna
- Peningkatan potensi kegagalan transaksi
- Bertambahnya beban operasional akibat retry dan komplain  

---

# Problem Statement
User experience buruk karena latency tinggi pada QRIS dan transaksi real-time, diperburuk kesenjangan jaringan 4G/5G di pedesaan.

## Dampak utama:
1. Transaksi sering timeout atau perlu retry  
2. Pengguna ragu apakah transaksi berhasil  
3. Biaya operasional meningkat (komplain, investigasi, retry traffic)  
4. Sulit melakukan diagnosis karena kurangnya metrik end-to-end  
5. Performa tidak stabil pada peak load  

---

# Tujuan Proyek (Feasible 3 SKS)
Mahasiswa membangun prototype sistem optimasi performa untuk alur QRIS/transaksi real-time yang:

1. Mengurangi latency p95 dan meningkatkan throughput pada beban uji  
2. Menerapkan caching yang tepat (read-heavy) dan strategi invalidasi  
3. Menerapkan proses async untuk bagian non-kritis (misalnya logging, notifikasi)  
4. Menyediakan observability (profiling, tracing, metrik) untuk menemukan bottleneck  
5. Mempertimbangkan kondisi jaringan pedesaan melalui simulasi latensi/jitter dan strategi mitigasi  

---

# Ruang Lingkup (In-Scope)

## A. Skenario Uji Minimal (pilih 2)
- Inquiry QRIS (cek merchant/nominal/validasi QR)  
- Payment QRIS (submit transaksi + status)  
- Realtime balance inquiry / status transaksi  

## B. Komponen Prototype yang Dibangun
1. API service untuk QRIS (simulasi) + endpoint real-time  
2. Database (mis. PostgreSQL/MySQL) dengan data dummy transaksi  
3. Caching layer (Redis/Memcached) untuk endpoint tertentu  
4. Message queue (mis. RabbitMQ/Kafka/Redis queue) untuk proses async  
5. Load testing & measurement (k6/JMeter/Locust) + laporan p50/p95/p99  
6. Network condition simulation (latency/jitter/packet loss) untuk “rural network”  

---

# Batasan (Out-of-Scope agar feasible)
- Tidak terhubung ke sistem bank/QRIS produksi (gunakan simulator/mocked services)  
- Tidak membangun mobile app penuh (cukup API + client sederhana atau Postman UI)  
- Tidak wajib implementasi 5G/telecom nyata (cukup simulasi network)  

---

# Peran per Prodi

## Teknik Informatika
- Algoritma caching dan predictive prefetching  
- Optimization query dan database indexing  
- Async processing dan message queue design  
- Performance profiling dan bottleneck analysis  

## Teknologi Informasi
- Implementation caching layer (Redis, Memcached)  
- Database optimization dan connection pooling  
- API optimization dan payload compression  
- CDN implementation untuk static assets  

## Teknik Komputer
- Infrastructure performance tuning  
- Network latency measurement dan optimization  
- Edge computing untuk mendekatkan processing ke user  
- Load testing dan capacity planning  